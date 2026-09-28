# aebello-svc

Backend API for Aebello, built with Go and PostgreSQL.

## Local development

Run these commands from this repository:

```sh
cp .env.example .env
docker compose up -d
go run ./cmd/api
```

Set ESIM_ACCESS_CODE in .env before starting. The API listens on port 8080.

## Backend

A small Go service: standard-library router, pgx, plain SQL, goose migrations (run automatically on start).

```
svc/
├── cmd/api/main.go          # wiring and HTTP server
├── internal/catalog/        # plan list from eSIM Access, pricing, /v1/destinations, /v1/plans
├── internal/auth/           # accounts and bearer-token sessions
├── internal/orders/         # checkout, payment confirmation, eSIM delivery, My eSIMs
├── internal/esimaccess/     # eSIM Access API client
├── internal/paystack/       # Paystack API client
├── internal/db/             # connection pool and migrations
├── pricing.json             # markup rules (see below)
├── Dockerfile               # production image
└── docker-compose.yml       # local Postgres
```

`````sh
go test ./...                 # unit tests
go vet ./...
```

**Pricing** is set in `pricing.json`, no code change needed:

```json
{ "markup": 2.0, "minProfit": 1.0, "overrides": { "AF-29": 1.6 }, "hidden": ["TG"] }
```

- `markup` multiplies the wholesale cost, and `minProfit` (USD) is the least earned per plan.
- `overrides` sets a different markup for a plan slug (`NG_1_7`), a country (`NG`) or a region (`AF-29`, the Africa plan that uses MTN in Nigeria).
- `hidden` stops selling plans by slug or location (Togo is hidden because it is very expensive wholesale).
- Prices round up to end in .49 or .99.

**API**

| Method | Path | |
| --- | --- | --- |
| GET | `/v1/destinations?q=` | All destinations with "from" price and networks |
| GET | `/v1/destinations/{code}` | One destination and every plan that works there |
| GET | `/v1/plans/{slug}?country=` | One plan |
| POST | `/v1/auth/register`, `/v1/auth/login` | Returns `{token, user}` |
| POST | `/v1/auth/logout` | 🔒 |
| GET | `/v1/me` | 🔒 |
| POST | `/v1/orders` | 🔒 `{planSlug, country}` → `{order, paymentUrl}` |
| GET | `/v1/orders`, `/v1/orders/{id}` | 🔒 order status (checks Paystack while pending) |
| GET | `/v1/esims`, `/v1/esims/{id}` | 🔒 eSIMs with status and usage |
| POST | `/v1/webhooks/paystack` | Paystack webhook (signature checked) |

🔒 = `Authorization: Bearer <token>`. Errors are `{"error", "message", "requestId"}`.

### Deploying to DigitalOcean

The simplest route is **App Platform** with a **Managed PostgreSQL** database:

1. Create the database; copy its connection string.
2. Create an app from `heismyke/aebello-svc`, source directory `/`, using the Dockerfile. HTTP port `8080`, health check `/healthz`.
3. Set environment variables:

| Variable | Value |
| --- | --- |
| `DATABASE_URL` | the managed database connection string |
| `ESIM_ACCESS_CODE` | eSIM Access → Developer → Access Code |
| `PAYSTACK_SECRET_KEY` | Paystack → Settings → API Keys (use `sk_test_…` first) |
| `WEB_URL` | the website, e.g. `https://aebello.vercel.app` |
| `ESIM_ORDERING_ENABLED` | `false` until you are ready to sell (see below) |
| `CHARGE_CURRENCY` | `NGN` (default). Prices are shown in USD and converted at checkout |
| `FX_BUFFER` | `0.03` (default): 3% added on top of the daily USD→NGN rate |
| `FX_FALLBACK_RATE` | Optional NGN per USD, used only if the rate feed is down at start-up |

4. In Paystack, set the webhook URL to `https://<your-api-domain>/v1/webhooks/paystack`.
5. On Vercel, set `VITE_API_URL` to the API's URL and redeploy the website.

A Droplet works too: install Docker, run the image with the same variables, and put Caddy or Nginx in front for HTTPS.
