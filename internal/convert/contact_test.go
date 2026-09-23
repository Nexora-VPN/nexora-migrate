package convert

import (
	"encoding/json"
	"testing"

	"github.com/nexora-vpn/nexora-migrate/internal/normalize"
)

// The customer's details are the half of a migration that used to be dropped:
// every source panel keeps an email or a Telegram id somewhere, and before
// Nexora had a column for them this tool read them, showed them in the review
// table and then wrote a user without them.

func TestSetContactSkipsEmptyValues(t *testing.T) {
	var c Client
	SetContact(&c, ContactEmail, "  a@b.com ")
	SetContact(&c, ContactPhone, "   ")
	SetContact(&c, ContactTelegramID, "")
	if len(c.Contact) != 1 || c.Contact[ContactEmail] != "a@b.com" {
		t.Fatalf("contact = %v", c.Contact)
	}
	// A source with nothing to say leaves the map nil, so the payload carries
	// no key at all rather than an empty object.
	var empty Client
	SetContact(&empty, ContactEmail, "")
	if empty.Contact != nil {
		t.Fatalf("contact = %v, want nil", empty.Contact)
	}
}

func TestClientItemCarriesTheContact(t *testing.T) {
	c := Client{SourceID: "1", Name: "customer", Enable: true}
	SetContact(&c, ContactEmail, "a@b.com")
	SetContact(&c, ContactTelegramID, "123456789")
	it := ClientItem(c, normalize.NewNamer())
	var payload map[string]any
	if err := json.Unmarshal(it.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	contact, ok := payload["contact"].(map[string]any)
	if !ok {
		t.Fatalf("payload = %s", it.Payload)
	}
	if contact["email"] != "a@b.com" || contact["telegram_id"] != "123456789" {
		t.Fatalf("contact = %v", contact)
	}

	// And an account nobody recorded anything about carries no key, so the
	// panel stores an absent card rather than an empty object.
	bare := ClientItem(Client{SourceID: "2", Name: "bare"}, normalize.NewNamer())
	var barePayload map[string]any
	if err := json.Unmarshal(bare.Payload, &barePayload); err != nil {
		t.Fatal(err)
	}
	if _, ok := barePayload["contact"]; ok {
		t.Fatalf("an account with no details carries %s", bare.Payload)
	}
}

// TestXrayClientsCarryTheirPerson: 3x-ui keeps a Telegram id and a comment on
// each client and this tool read neither.
func TestXrayClientsCarryTheirPerson(t *testing.T) {
	settings := Obj{"clients": []any{
		Obj{"email": "someone", "id": "uuid-1", "tgId": "123456789", "comment": "paid in cash"},
		// The same field as a JSON number, which some forks write.
		Obj{"email": "other", "id": "uuid-2", "tgId": float64(987654321)},
	}}
	clients := clientsFrom("vless", settings)
	if len(clients) != 2 {
		t.Fatalf("clients = %d", len(clients))
	}
	if clients[0].TelegramID != "123456789" || clients[0].Comment != "paid in cash" {
		t.Fatalf("client = %+v", clients[0])
	}
	if clients[1].TelegramID != "987654321" {
		t.Fatalf("a numeric tgId read as %q", clients[1].TelegramID)
	}
}
