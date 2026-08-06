# Routing Configuration Failures

## Symptoms

Delivery attempts receive `404`, route-specific `400` responses, or connection failures
after a destination URL or route change. The event payload may be valid while the target
endpoint is wrong.

## Likely causes

The configured receipt URL may point to the wrong environment, missing path, retired
route, or tenant-specific endpoint. A source or event type may also be mapped to the
wrong destination configuration.

## Checks

Verify the destination URL, path, environment, and route mapping for the affected source
and event type. Confirm the route expects receipt creation requests and supports the
configured authentication method. Do not paste credentials or full accounting payloads
into tickets.

## Safe remediation

Correct the destination URL or route mapping through configuration review. Confirm the
destination accepts a small sanitized receipt request before recovering failed events.

## Redrive readiness

Do not redrive until the route is corrected and the destination owner confirms the failed
requests did not apply receipts elsewhere. Redrive remains a human-controlled action.
