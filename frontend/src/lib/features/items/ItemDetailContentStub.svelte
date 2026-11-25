<script>
  // Minimal ItemDetailContent stand-in. It exposes the item identity the
  // component is currently displaying and forwards the header's "Log time"
  // action so tests can drive the real ItemDetail handler.
  let { item, onlogTime = null } = $props();
</script>

<div data-testid="item-detail-ready">{item?.title ?? ''}</div>
<button type="button" data-testid="log-time-btn" onclick={() => onlogTime?.()}>log time</button>
