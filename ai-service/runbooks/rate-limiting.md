# Rate Limiting

## Symptoms

The destination returns `429` or a throttling response while EventRail is delivering
receipt events. Delivery attempts may cluster during backfills, replays, or bursts from
the accounting producer.

## Likely causes

EventRail may be exceeding the destination's allowed request rate, or the destination
may be enforcing a temporary tenant-level quota. Replay activity can amplify normal
traffic if it is started while new receipts are still flowing.

## Checks

Review the delivery attempt timestamps, response codes, retry counts, and whether replay
or redrive was recently triggered. Ask the destination owner for the active rate limit
and reset window. Do not include full receipt payloads in the investigation notes.

## Safe remediation

Pause human-initiated replay or redrive activity if it is contributing to pressure.
Coordinate a lower delivery rate or wait for the destination quota window to reset.
Keep new producer changes separate from historical recovery.

## Redrive readiness

Do not redrive until the destination has capacity and the operator has selected a safe
batch size. Redrive remains a human-controlled action.
