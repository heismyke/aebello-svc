// Package esimaccess is a small client for the eSIM Access reseller API
// (https://docs.esimaccess.com). Every call is a POST with a JSON body and the
// RT-AccessCode header. Prices are USD x 10,000 and data volumes are bytes.
package esimaccess

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

const baseURL = "https://api.esimaccess.com/api/v1/open/"

// ErrNotReady means the profiles of a new order are still being allocated.
// The API answers this for up to ~30 seconds after ordering.
var ErrNotReady = errors.New("esimaccess: profiles are still being allocated")

// Error is a failure reported by the API itself.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("esimaccess: %s (%s)", e.Message, e.Code) }

type Client struct {
	accessCode string
	http       *http.Client
}

func New(accessCode string) *Client {
	// The full plan list is several MB and can take over 30s to download.
	return &Client{accessCode: accessCode, http: &http.Client{Timeout: 2 * time.Minute}}
}

type Operator struct {
	Name        string `json:"operatorName"`
	NetworkType string `json:"networkType"`
}

type LocationNetwork struct {
	Name      string     `json:"locationName"`
	Code      string     `json:"locationCode"`
	Operators []Operator `json:"operatorList"`
}

// Package is a data plan as sold by eSIM Access.
type Package struct {
	Slug         string            `json:"slug"`
	Name         string            `json:"name"`
	Price        int64             `json:"price"`  // USD x 10,000
	Volume       int64             `json:"volume"` // bytes
	DataType     int               `json:"dataType"`
	Duration     int               `json:"duration"`
	DurationUnit string            `json:"durationUnit"`
	Location     string            `json:"location"`     // comma-separated ISO codes covered
	LocationCode string            `json:"locationCode"` // "NG" for one country, "AF-29" for a region
	Speed        string            `json:"speed"`
	IPExport     string            `json:"ipExport"`
	TopUpType    int               `json:"supportTopUpType"`
	Networks     []LocationNetwork `json:"locationNetworkList"`
}

// Profile is an allocated eSIM.
type Profile struct {
	EsimTranNo  string `json:"esimTranNo"`
	OrderNo     string `json:"orderNo"`
	ICCID       string `json:"iccid"`
	AC          string `json:"ac"` // activation code, LPA:1$...
	QRCodeURL   string `json:"qrCodeUrl"`
	SMDPStatus  string `json:"smdpStatus"`
	EsimStatus  string `json:"esimStatus"`
	ExpiredTime string `json:"expiredTime"`
	TotalVolume int64  `json:"totalVolume"`
	OrderUsage  int64  `json:"orderUsage"`
}

// Expires parses ExpiredTime, which uses a +0000 style offset.
func (p Profile) Expires() *time.Time {
	t, err := time.Parse("2006-01-02T15:04:05-0700", p.ExpiredTime)
	if err != nil {
		return nil
	}
	return &t
}

type Usage struct {
	EsimTranNo string `json:"esimTranNo"`
	DataUsage  int64  `json:"dataUsage"`
	TotalData  int64  `json:"totalData"`
}

// Packages returns every standard (non top-up) plan on offer.
func (c *Client) Packages(ctx context.Context) ([]Package, error) {
	var out struct {
		PackageList []Package `json:"packageList"`
	}
	err := c.call(ctx, "package/list", map[string]any{"type": "BASE"}, &out)
	return out.PackageList, err
}

// Order buys one eSIM. transactionID makes the call idempotent: repeating it
// returns the original order instead of buying twice.
func (c *Client) Order(ctx context.Context, transactionID, slug string, price int64) (string, error) {
	var out struct {
		OrderNo string `json:"orderNo"`
	}
	err := c.call(ctx, "esim/order", map[string]any{
		"transactionId":   transactionID,
		"amount":          price,
		"packageInfoList": []map[string]any{{"packageCode": slug, "count": 1, "price": price}},
	}, &out)
	return out.OrderNo, err
}

// ProfilesByOrder returns the eSIMs of an order, or ErrNotReady.
func (c *Client) ProfilesByOrder(ctx context.Context, orderNo string) ([]Profile, error) {
	return c.query(ctx, map[string]any{"orderNo": orderNo})
}

// Profile returns the current state of one eSIM.
func (c *Client) Profile(ctx context.Context, esimTranNo string) (Profile, error) {
	list, err := c.query(ctx, map[string]any{"esimTranNo": esimTranNo})
	if err != nil {
		return Profile{}, err
	}
	if len(list) == 0 {
		return Profile{}, &Error{Code: "not_found", Message: "eSIM not found"}
	}
	return list[0], nil
}

func (c *Client) query(ctx context.Context, filter map[string]any) ([]Profile, error) {
	filter["pager"] = map[string]int{"pageNum": 1, "pageSize": 50}
	var out struct {
		EsimList []Profile `json:"esimList"`
	}
	err := c.call(ctx, "esim/query", filter, &out)
	return out.EsimList, err
}

// Usage returns data used for up to 10 eSIMs. It lags real use by 2-3 hours.
func (c *Client) Usage(ctx context.Context, esimTranNos []string) ([]Usage, error) {
	var out struct {
		List []Usage `json:"esimUsageList"`
	}
	err := c.call(ctx, "esim/usage/query", map[string]any{"esimTranNoList": esimTranNos}, &out)
	return out.List, err
}

// Balance returns the prepaid account balance in USD x 10,000.
func (c *Client) Balance(ctx context.Context) (int64, error) {
	var out struct {
		Balance int64 `json:"balance"`
	}
	err := c.call(ctx, "balance/query", map[string]any{}, &out)
	return out.Balance, err
}

func (c *Client) call(ctx context.Context, path string, body, out any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("RT-AccessCode", c.accessCode)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("esimaccess %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("esimaccess %s: HTTP %d", path, resp.StatusCode)
	}

	var env struct {
		Success   bool            `json:"success"`
		ErrorCode any             `json:"errorCode"` // string, number or null
		ErrorMsg  string          `json:"errorMsg"`
		Obj       json.RawMessage `json:"obj"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("esimaccess %s: decode: %w", path, err)
	}
	if !env.Success {
		code := fmt.Sprint(env.ErrorCode)
		if code == "200010" {
			return ErrNotReady
		}
		return &Error{Code: code, Message: env.ErrorMsg}
	}
	if out == nil || len(env.Obj) == 0 {
		return nil
	}
	return json.Unmarshal(env.Obj, out)
}
