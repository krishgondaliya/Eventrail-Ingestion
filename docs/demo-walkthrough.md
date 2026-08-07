# EventRail Interview Demo

## Before the interview

Commands:

```powershell
.\scripts\demo.ps1 reset -Force -NoBrowser
.\scripts\demo.ps1 status
```

Checklist:

- All expected services are healthy.
- Dashboard opens.
- No stale event is displayed.
- Scenario buttons work.
- Validation-failure and recovery controls are available.
- Deterministic runbook analysis is the default.
- Ollama is optional and not required for the demo.

## 0:00-0:45 - Customer problem

Talking points:

- A customer paid invoice INV-2048 for $500.
- The payment service must notify the receipt system.
- Lost delivery means no receipt or stale accounting.
- Duplicate delivery could produce duplicate side effects.
- EventRail acts like certified mail for financial software.

Keep the explanation nontechnical.

## 0:45-1:45 - Healthy delivery

Dashboard actions:

1. Select Healthy.
2. Click Run Demo.
3. Point to:
   - Received
   - Safely stored
   - Published
   - Processing
   - Delivered

Talking point:

The event is stored durably before asynchronous delivery begins.

Do not begin with PostgreSQL or Redis.

## 1:45-3:00 - Temporary failure

Dashboard actions:

1. Select Temporary failure.
2. Click Run Demo.
3. Point out:
   - First 503 attempt
   - Recovering automatically
   - Second successful attempt
   - Delivered

Talking point:

Temporary failures recover automatically without asking the customer to submit the payment again.

## 3:00-5:15 - Permanent validation failure

Dashboard actions:

1. Select Validation failure.
2. Click Run Demo.
3. Wait for Needs attention.
4. Show:
   - The failed 400 delivery attempt
   - Missing invoice_id explanation
   - The AI or deterministic guidance card
   - Recommended checks
   - Redrive readiness
   - Trusted runbook citation

Talking points:

- Automatic recovery stops safely instead of retrying forever.
- The assistant receives only sanitized operational metadata.
- It retrieves trusted runbooks.
- Guidance is advisory.
- It cannot modify or redrive the event.

Use accurate language for the displayed mode:

For deterministic mode:

This is deterministic runbook guidance.

For Ollama success:

This is locally generated guidance grounded in trusted runbooks.

Do not claim a live LLM was used unless the dashboard displays Local LLM grounded analysis.

## 5:15-6:30 - Human-controlled recovery

Dashboard actions:

1. Click Fix destination.
2. Click Redrive event.
3. Point to:
   - Redriven by operator
   - New successful attempt
   - Delivered

Talking point:

Recovery remains human-controlled. EventRail reuses the stable event identity so downstream systems can protect themselves from duplicate side effects.

## 6:30-7:20 - Reliability guarantees

Explain:

- PostgreSQL is the authoritative record.
- The event and publication intent are stored atomically.
- Redis downtime cannot lose an accepted event.
- Delivery is at least once, not exactly once.
- Worker crashes and temporary failures are recoverable.
- AI is outside the reliability path.

Keep this section short unless the interviewer asks for a deeper technical explanation.

## 7:20-8:00 - Final takeaway

Use this exact closing:

EventRail ensures an important payment event is not lost, duplicated, or forgotten, and helps an operator understand what went wrong when automatic recovery is no longer enough.

## When something goes wrong

### Dashboard unavailable

Run:

```powershell
.\scripts\demo.ps1 status
```

Check:

```text
.demo/logs/dashboard.stderr.log
.demo/logs/dashboard.stdout.log
```

### EventRail API unavailable

Check:

```text
.demo/logs/api.stderr.log
.demo/logs/api.stdout.log
```

Use Fixture Preview only as an explicitly labeled backup.

Do not pretend fixture data is live.

### AI service unavailable

Continue the demo.

Say:

The event remains durable and redrivable even when automated analysis is unavailable.

Show that Fix destination and Redrive still work.

### Ollama is slow

Do not wait for it during the interview.

Restart with deterministic mode:

```powershell
.\scripts\demo.ps1 stop
.\scripts\demo.ps1 start -NoBrowser
```

Explain that the local model provider is optional and deterministic grounded guidance is the reliable fallback.

### Stale data

Run:

```powershell
.\scripts\demo.ps1 reset -Force
```

### Complete cleanup

Run:

```powershell
.\scripts\demo.ps1 stop
```

## Claims to make

Safe claims:

- Transactional event and outbox storage
- At-least-once delivery
- Stable event identity
- Retry and DLQ recovery
- Human-controlled redrive
- Sanitized runbook-grounded guidance
- Optional local LLM provider
- Deterministic fallback
- AI outage does not affect event recovery

Claims to avoid:

- End-to-end exactly once
- Autonomous remediation
- Guaranteed prevention of all duplicate delivery attempts
- Production-scale multi-region architecture
- Live LLM use when deterministic mode is displayed
- Ollama latency being suitable for every machine
