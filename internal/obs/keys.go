package obs

// KeyReloads counts key reloads so an operator can alert on a rotation that did not take.
type KeyReloads struct{ c *Counter }

// NewKeyReloads registers agentgw_key_reloads_total{kind,result} on r. kind is studio, admin or
// backend. result is ok or error. It lives in its own file so it does not touch the standard set.
func NewKeyReloads(r *Registry) *KeyReloads {
	return &KeyReloads{c: r.NewCounter("agentgw_key_reloads_total", "Key reloads by kind (studio, admin, backend) and result (ok, error).", "kind", "result")}
}

// Inc records one reload. A nil receiver is a no-op, so callers need no guard in tests.
func (k *KeyReloads) Inc(kind, result string) {
	if k == nil {
		return
	}
	k.c.Inc(kind, result)
}
