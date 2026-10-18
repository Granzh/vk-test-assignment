// Package admin holds administrator-only features: partner and day setup,
// promo code upload, stats and CSV exports, all gated by ADMIN_IDS and
// recorded in audit_log.
package admin

// InputError is a problem with what the admin sent. Its message is already
// formatted for the admin (HTML) and says what is wrong and what is expected.
type InputError struct{ Msg string }

func (e *InputError) Error() string { return e.Msg }
