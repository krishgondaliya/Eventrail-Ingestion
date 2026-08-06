# Authentication and Authorization Failures

## Symptoms

Delivery attempts receive `401` or `403` responses from the receipt destination.
EventRail may move events to retry or DLQ depending on the failure classification and
retry count.

## Likely causes

The destination token may be expired, missing, scoped incorrectly, or rejected by a
recent authorization change. A `403` can also mean the endpoint is reachable but the
configured credential lacks permission to create receipts for the target tenant.

## Checks

Confirm the destination configuration references the expected credential name and
environment. Ask the destination owner to verify token status and scopes without sharing
the secret value. Compare the affected source and event type against allowed receipt
routes.

## Safe remediation

Rotate or correct the credential through the approved secret-management process. Confirm
the destination service accepts a small non-sensitive receipt validation request before
touching failed production events.

## Redrive readiness

Do not redrive until authentication or authorization is fixed and the destination owner
confirms the failed receipts were not already applied. Redrive remains a
human-controlled action.
