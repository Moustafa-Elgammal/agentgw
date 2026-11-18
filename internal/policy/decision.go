package policy

import "fmt"

// Decision is the outcome of a policy check.
type Decision struct {
	Allowed bool
	Code    string
	Message string
}

// Allow is the passing decision.
func Allow() Decision { return Decision{Allowed: true} }

// Deny builds a refusal with a machine-readable code and a message written for the agent.
func Deny(code, format string, a ...any) Decision {
	return Decision{Code: code, Message: fmt.Sprintf(format, a...)}
}

// The refusals below are shared by every store, so the agent sees the same
// code and wording whichever store is behind the gateway.

// TierDisabled refuses a sensitive tool.
func TierDisabled(tool string) Decision {
	return Deny("tier_disabled", "%s is a sensitive action and is disabled", tool)
}

// SwitchedOff refuses a call matched by a kill switch.
func SwitchedOff(tool string) Decision {
	return Deny("switched_off", "%s is switched off by the game right now. Do not retry until the game turns it back on.", tool)
}

// RateLimited refuses an attempt over the per-minute budget. kind is "read" or "action".
func RateLimited(kind string, max int) Decision {
	return Deny("rate_limited", "max %d %ss per minute. Wait and retry.", max, kind)
}

// Cooldown refuses a call made too soon after the last successful one.
func Cooldown(tool string, seconds int) Decision {
	return Deny("cooldown", "%s is available again in %d seconds", tool, seconds)
}

// DailyCallCap refuses a call over the per-day call count.
func DailyCallCap(tool string, allowed int) Decision {
	return Deny("daily_call_cap", "%s: %d calls allowed per day, all used", tool, allowed)
}

// DailyAmountCap refuses a call that would exceed the per-day amount.
func DailyAmountCap(tool string, need, left int64) Decision {
	return Deny("daily_amount_cap", "%s: %d needed, %d left today", tool, need, left)
}
