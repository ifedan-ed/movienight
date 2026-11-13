package schema

// ---- Digest emails (rw/digests) ----

// EmailSettings is which digests the person gets, and where.
type EmailSettings struct {
	Weekly  bool    `json:"weekly"`
	Monthly bool    `json:"monthly"`
	Yearly  bool    `json:"yearly"`
	Address *string `json:"address" required:"false" nullable:"true"`
}

// EmailSettingsIn changes some of them (null: leave as it is).
type EmailSettingsIn struct {
	_       struct{} `additionalProperties:"true"`
	Weekly  *bool    `json:"weekly" required:"false" nullable:"true"`
	Monthly *bool    `json:"monthly" required:"false" nullable:"true"`
	Yearly  *bool    `json:"yearly" required:"false" nullable:"true"`
	Address *string  `json:"address" required:"false" nullable:"true" maxLength:"320"`
}

// DigestOut is one digest, without its email.
type DigestOut struct {
	ID          int32   `json:"id"`
	Kind        string  `json:"kind" enum:"week,month,year"`
	Subject     string  `json:"subject"`
	PeriodStart string  `json:"period_start"`
	PeriodEnd   string  `json:"period_end"`
	Model       *string `json:"model" required:"false" nullable:"true"`
	SentAt      *string `json:"sent_at" required:"false" nullable:"true"`
	SentTo      *string `json:"sent_to" required:"false" nullable:"true"`
}

// SentOut is where "Send it to me now" sent it.
type SentOut struct {
	Ok     bool   `json:"ok" default:"true" required:"false"`
	SentTo string `json:"sent_to"`
}
