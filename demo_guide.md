# EventRail Ingestion - Demo Guide

This guide provides a step-by-step approach to demonstrate the key features of the `eventrail-ingestion` project, which includes the API, PostgreSQL storage, Redis Streams, and background workers.

## Step 1: Start the Infrastructure

First, bring up the API, PostgreSQL, and Redis containers using Docker Compose.

```bash
docker compose up -d --build
```

Wait a few seconds for the database and cache to be healthy. The API will wait for them before starting.

*(Optional but recommended)*: Open a separate terminal window and follow the API logs so you can show the background stream worker processing events in real time:
```bash
docker compose logs -f api
```

## Step 2: Apply Database Migrations

The database tables need to be created before accepting events. Run the provided SQL migrations inside the Postgres container:

```bash
docker exec -it eventrail-postgres psql -U eventrail -d eventrail -f /migrations/001_create_events_table.sql
docker exec -it eventrail-postgres psql -U eventrail -d eventrail -f /migrations/002_add_idempotency.sql
```

## Step 3: Check System Health

Demonstrate that the API and its dependencies are fully operational.

```bash
curl -s http://localhost:8080/health
```
**Expected Output:** `{"status":"ok","postgres":"ok","redis":"ok"}`

## Step 4: Ingest a Normal Event

Show the core functionality: ingesting an event, persisting it to Postgres, and publishing it to Redis Streams.

```bash
curl -s -X POST http://localhost:8080/events \
  -H "Content-Type: application/json" \
  -d '{
    "event_type": "user.signup",
    "source": "frontend",
    "payload": {"user_id": "123", "email": "test@example.com"}
  }'
```
**Observation:** You will receive a JSON response with an `id`. If you look at the terminal running `docker compose logs -f api`, you will see the background worker process the event (`processed event stream_id=...`).

## Step 5: Read the Event from the Database

Show that the event is durably stored in PostgreSQL.

```bash
# Replace <ID> with the id returned from Step 4
curl -s http://localhost:8080/events/<ID>
```

## Step 6: Demonstrate Idempotency

Send the exact same request twice, but include an `Idempotency-Key` header. This shows that the system safely handles duplicate requests without creating duplicate database rows or duplicate stream events.

```bash
curl -s -X POST http://localhost:8080/events \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: signup-request-abc" \
  -d '{
    "event_type": "user.signup",
    "source": "mobile_app",
    "payload": {"user_id": "999"}
  }'
```
Run this command multiple times. You will get the **same `id`** back each time, proving the API acts idempotently.

## Step 7: Demonstrate Retries and DLQ (Dead Letter Queue)

The system has a built-in failure simulation for testing. If you send an event with `event_type: "force.fail"`, the background worker will intentionally fail to process it.

```bash
curl -s -X POST http://localhost:8080/events \
  -H "Content-Type: application/json" \
  -d '{
    "event_type": "force.fail",
    "source": "payment_gateway",
    "payload": {"amount": 500}
  }'
```
**Observation:** Watch the API logs (`docker compose logs -f api`). You will see it attempt to process the event, fail, and schedule a retry using exponential backoff (publishing to a Redis ZSET). After the maximum number of retries (default 5), it will move the event to the Dead Letter Queue (`eventrail.events.dlq`).

## Step 8: Demonstrate Replay Mechanism

Show how the system can recover or backfill events by replaying historical data from PostgreSQL back into the Redis stream.

```bash
# This will query events from Postgres within the time range and re-publish them to Redis
curl -s -X POST http://localhost:8080/replay \
  -H "Content-Type: application/json" \
  -d '{
    "from": "2024-01-01T00:00:00Z",
    "to": "2026-12-31T23:59:59Z",
    "limit": 10
  }'
```
**Observation:** The API logs will show the worker re-processing these replayed events.

## Step 9: Cleanup

Once the demo is complete, tear down the environment to leave a clean state:

```bash
docker compose down -v
```
