# Receipt Validation Failures

## Symptoms

The receipt destination returns `400` for accounting receipts that EventRail delivered
successfully at the transport layer. The most common signal is a response body or DLQ
entry that points to a missing `invoice_id`, missing `amount`, missing `currency`, or
another required receipt field.

## Likely causes

The upstream event was accepted into EventRail with a payload that the destination
receipt service cannot apply. This can happen when a producer omits receipt fields,
sends an empty invoice identifier, or maps accounting fields from the wrong source
object.

## Checks

Inspect the stored event metadata, delivery attempt response code, and DLQ record.
Compare field names against the receipt contract using sanitized examples only. Verify
that the original producer can supply `invoice_id`, `amount`, and `currency` without
exposing customer data or full payment payloads.

## Safe remediation

Correct the producing service or transformation so future receipt events include the
required fields. If a historical event must be repaired, create a sanitized replacement
event through the normal ingestion path rather than editing database rows by hand.

## Redrive readiness

Do not redrive until the payload or producer defect is corrected and the destination
owner confirms the receipt can be applied exactly once. Redrive remains a
human-controlled action.
