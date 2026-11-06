package news

import (
	"fmt"
	"reflect"
	"slices"
	"time"
)

type options struct {
	defaultURL  string
	environment string
	mailTo      []string
	smsPhones   []string
	smsTemplate string
	webhook     WebhookSender
	mail        MailSender
	sms         SMSSender
	report      func(Delivery)
	now         func() time.Time
}

// Option configures a notifier. Later options override earlier ones.
type Option func(*options)

// WithEnvironment sets the environment included in alert messages.
func WithEnvironment(environment string) Option {
	return func(o *options) { o.environment = environment }
}

// WithReport selects delivery diagnostics; nil uses ReportDelivery.
func WithReport(report func(Delivery)) Option {
	return func(o *options) { o.report = report }
}

// WithClock selects the message timestamp source; nil uses time.Now.
func WithClock(now func() time.Time) Option {
	return func(o *options) { o.now = now }
}

// ValidateSender rejects nil interfaces and typed nils at assembly boundaries.
// Reflection is confined here: arbitrary implementations can have any nilable
// concrete type. No method is invoked and no sending path uses reflection.
func ValidateSender(sender any) error {
	if sender == nil {
		return ErrSenderMissing
	}
	value := reflect.ValueOf(sender)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return ErrSenderMissing
		}
	}
	return nil
}

// New builds an injected notifier without constructing or owning any sender.
func New(opts ...Option) (*Notifier, error) {
	var o options
	for i, option := range opts {
		if option == nil {
			return nil, fmt.Errorf("notification option %d is nil", i)
		}
		option(&o)
	}
	for _, sender := range []any{o.webhook, o.mail, o.sms} {
		if sender != nil {
			if err := ValidateSender(sender); err != nil {
				return nil, err
			}
		}
	}
	now := o.now
	if now == nil {
		now = time.Now
	}
	report := o.report
	if report == nil {
		report = ReportDelivery
	}
	return &Notifier{
		defaultURL:  o.defaultURL,
		envName:     o.environment,
		now:         func() string { return now().Format(time.DateTime) },
		report:      report,
		webhook:     o.webhook,
		mail:        o.mail,
		mailTo:      slices.Clone(o.mailTo),
		sms:         o.sms,
		smsPhones:   slices.Clone(o.smsPhones),
		smsTemplate: o.smsTemplate,
	}, nil
}
