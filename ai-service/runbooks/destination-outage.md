# Destination Outage

## Symptoms

Delivery attempts fail with timeouts, connection errors, DNS failures, TLS handshake
errors, or destination `5xx` responses. Multiple receipt events may fail across sources
at roughly the same time.

## Likely causes

The receipt destination may be down, degraded, unreachable from the EventRail runtime,
or overloaded. Network routing, certificate renewal, or load balancer changes can also
cause transport failures without a payload defect.

## Checks

Check readiness for EventRail, recent delivery attempt outcomes, and whether failures
span several event types. Confirm destination health through its owner rather than
assuming the stored receipt payload is bad. Record timestamps and response classes, not
private receipt details.

## Safe remediation

Wait for the destination to recover or coordinate with the destination team on a
controlled recovery window. Avoid changing receipt payloads for pure transport or `5xx`
failures.

## Redrive readiness

Do not redrive until the destination is healthy, reachable, and ready for duplicate-safe
receipt delivery by stable event ID. Redrive remains a human-controlled action.
