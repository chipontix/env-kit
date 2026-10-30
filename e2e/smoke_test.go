// Package e2e checks the stack `make up` started: the API container, its
// database and the seed data, over the network.
package e2e

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

func url(t *testing.T, path string) string {
	base := os.Getenv("API_URL")
	if base == "" {
		t.Skip("API_URL not set; run through `make test`")
	}
	return base + path
}

var client = &http.Client{Timeout: 5 * time.Second}

func TestHealthz(t *testing.T) {
	res, err := client.Get(url(t, "/healthz"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("GET /healthz = %d", res.StatusCode)
	}
}

func TestSeededStackTakesAnOrder(t *testing.T) {
	res, err := client.Get(url(t, "/products"))
	if err != nil {
		t.Fatal(err)
	}
	var products []struct {
		ID    int64  `json:"id"`
		SKU   string `json:"sku"`
		Stock int    `json:"stock"`
	}
	json.NewDecoder(res.Body).Decode(&products)
	res.Body.Close()
	if len(products) < 6 {
		t.Fatalf("GET /products returned %d products, want the 6 seeded ones", len(products))
	}
	var cable int64
	for _, p := range products {
		if p.SKU == "CB-2M" {
			cable = p.ID
		}
	}
	if cable == 0 {
		t.Fatal("seeded product CB-2M missing")
	}

	body, _ := json.Marshal(map[string]any{"customer_id": 1, "items": []map[string]any{{"product_id": cable, "qty": 1}}})
	res, err = client.Post(url(t, "/orders"), "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var o struct {
		ID         int64 `json:"id"`
		TotalCents int64 `json:"total_cents"`
	}
	json.NewDecoder(res.Body).Decode(&o)
	res.Body.Close()
	if res.StatusCode != http.StatusCreated || o.TotalCents != 990 {
		t.Fatalf("POST /orders = %d, order %+v; want 201 with total 990", res.StatusCode, o)
	}

	// Cancel it so repeated runs do not drain the seeded stock.
	res, err = client.Post(url(t, "/orders/"+strconv.FormatInt(o.ID, 10)+"/cancel"), "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("cancel order %d = %d", o.ID, res.StatusCode)
	}
}
