# Schema Errors

## Symptoms

The receipt destination rejects events for malformed JSON, unsupported schema version,
or invalid field types such as a string `amount` where a numeric value is required.

## Likely causes

A producer or transformation may have emitted invalid JSON, changed schema version
without coordination, or serialized accounting fields with the wrong type. These issues
usually affect a specific source, event type, or deployment window.

## Checks

Inspect the stored payload shape using sanitized field names and types. Compare the
event version and receipt fields against the destination contract. Confirm whether the
producer deployed a serialization or schema change near the first failure.

## Safe remediation

Fix the producer or mapper and validate with a non-sensitive sample event. For affected
historical receipts, prefer re-emitting corrected events through ingestion after the
schema defect is understood.

## Redrive readiness

Do not redrive malformed or incompatible payloads. Redrive remains a human-controlled
action after the schema mismatch is corrected and duplicate handling has been confirmed.
