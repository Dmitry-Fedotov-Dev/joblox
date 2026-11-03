package order

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Requisites are the owner's payment details. They live in their own file outside the profile, so the
// profile can sit in a public repository and the requisites never do. They are shown only on the page of a
// verified, approved order, with that order's reference in the payment purpose.
type Requisites struct {
	Title     string         `yaml:"title" json:"title,omitempty"`
	Recipient string         `yaml:"recipient" json:"recipient"`
	Lines     []RequisiteRow `yaml:"lines" json:"lines"`
	Purpose   string         `yaml:"purpose" json:"purpose"` // template; {ref} becomes the order reference, {total} the sum
	Note      string         `yaml:"note" json:"note,omitempty"`
}

type RequisiteRow struct {
	Label string `yaml:"label" json:"label"`
	Value string `yaml:"value" json:"value"`
}

// LoadRequisites reads the requisites file; an empty path means the shop takes no transfers.
// Check says what is missing in requisites typed by hand.
func (r *Requisites) Check() error {
	if r.Recipient == "" || len(r.Lines) == 0 {
		return errors.New("укажите получателя и хотя бы одну строку: куда переводить")
	}
	if !strings.Contains(r.Purpose, "{ref}") {
		return errors.New("назначение платежа должно содержать {ref}: по нему вы найдёте оплату")
	}
	return nil
}

func LoadRequisites(path string) (*Requisites, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path) // #nosec G304 -- the path comes from the operator's configuration
	if err != nil {
		return nil, err
	}
	var r Requisites
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("requisites: %w", err)
	}
	if r.Recipient == "" || len(r.Lines) == 0 {
		return nil, errors.New("requisites: recipient and at least one line are required")
	}
	if !strings.Contains(r.Purpose, "{ref}") {
		return nil, errors.New("requisites: purpose must contain {ref}, otherwise payments cannot be matched to orders")
	}
	r.Split()
	return &r, nil
}

// Split makes one row of every line of a value: «Демир Банк\nТелефон: +996…\nПолучатель: Дмитрий Ф.» typed
// into one field becomes the rows Банк, Телефон and the recipient, each copied on its own. A «\n» typed as two
// characters (copied from an .env file) counts as a line break too.
func (r *Requisites) Split() {
	var rows []RequisiteRow
	for _, row := range r.Lines {
		parts := strings.Split(strings.ReplaceAll(row.Value, `\n`, "\n"), "\n")
		if len(parts) == 1 {
			rows = append(rows, row)
			continue
		}
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			label, value := row.Label, part
			if l, v, ok := strings.Cut(part, ": "); ok && len([]rune(l)) <= 24 {
				label, value = strings.TrimSpace(l), strings.TrimSpace(v)
			}
			if strings.EqualFold(label, "Получатель") {
				r.Recipient = value // the recipient written in the value is the one the bank shows
				continue
			}
			rows = append(rows, RequisiteRow{Label: label, Value: value})
		}
	}
	r.Lines = rows
}

// Fingerprint is a short hash of everything a payer would copy. The bot reports it on every start: if someone
// replaced the account number on the server, the operator sees a different fingerprint at once.
func (r *Requisites) Fingerprint() string {
	if r == nil {
		return "нет реквизитов"
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n", r.Recipient, r.Purpose)
	for _, l := range r.Lines {
		fmt.Fprintf(h, "%s=%s\n", l.Label, l.Value)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// PurposeFor fills the payment purpose for one order.
func (r *Requisites) PurposeFor(ref, total string) string {
	return strings.NewReplacer("{ref}", ref, "{total}", total).Replace(r.Purpose)
}
