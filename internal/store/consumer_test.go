package store

import (
	"context"
	"testing"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

func TestPlanCRUD(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	id, err := db.UpsertPlan(ctx, model.Plan{Name: "free", RPS: 60, Burst: 60})
	if err != nil || id == 0 {
		t.Fatalf("insert plan: id=%d err=%v", id, err)
	}
	// Update by id.
	if _, err := db.UpsertPlan(ctx, model.Plan{ID: id, Name: "free", RPS: 100, Burst: 120, DailyQuota: 10000}); err != nil {
		t.Fatal(err)
	}
	plans, err := db.ListPlans(ctx)
	if err != nil || len(plans) != 1 {
		t.Fatalf("list plans: %d err=%v", len(plans), err)
	}
	if plans[0].RPS != 100 || plans[0].Burst != 120 || plans[0].DailyQuota != 10000 {
		t.Fatalf("plan not updated: %+v", plans[0])
	}

	got, ok, err := db.GetPlan(ctx, id)
	if err != nil || !ok || got.Name != "free" {
		t.Fatalf("get plan: %+v ok=%v err=%v", got, ok, err)
	}
	ok, err = db.DeletePlan(ctx, id)
	if err != nil || !ok {
		t.Fatalf("delete plan: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := db.GetPlan(ctx, id); ok {
		t.Fatal("plan still present after delete")
	}
}

func TestConsumerCRUD(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	planID, _ := db.UpsertPlan(ctx, model.Plan{Name: "pro", RPS: 500, Burst: 1000})

	id, err := db.UpsertConsumer(ctx, model.Consumer{Name: "acme", PlanID: planID, Enabled: true})
	if err != nil || id == 0 {
		t.Fatalf("insert consumer: id=%d err=%v", id, err)
	}
	c, ok, err := db.GetConsumer(ctx, id)
	if err != nil || !ok || c.Name != "acme" || c.PlanID != planID || !c.Enabled {
		t.Fatalf("get consumer: %+v ok=%v err=%v", c, ok, err)
	}
	if _, err := db.UpsertConsumer(ctx, model.Consumer{ID: id, Name: "acme-corp", PlanID: planID, Enabled: false}); err != nil {
		t.Fatal(err)
	}
	consumers, _ := db.ListConsumers(ctx)
	if len(consumers) != 1 || consumers[0].Name != "acme-corp" || consumers[0].Enabled {
		t.Fatalf("consumer not updated: %+v", consumers)
	}
	ok, err = db.DeleteConsumer(ctx, id)
	if err != nil || !ok {
		t.Fatalf("delete consumer: ok=%v err=%v", ok, err)
	}
}

func TestAPIKeyLifecycle(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	cid, _ := db.UpsertConsumer(ctx, model.Consumer{Name: "acme", Enabled: true})

	hash := HashAPIKey("secret-key-123")
	keyID, err := db.CreateAPIKey(ctx, cid, "prod", hash)
	if err != nil || keyID == 0 {
		t.Fatalf("create key: id=%d err=%v", keyID, err)
	}

	// Listing returns metadata, never the hash/secret.
	keys, err := db.ListConsumerKeys(ctx, cid)
	if err != nil || len(keys) != 1 || keys[0].Name != "prod" || !keys[0].Enabled {
		t.Fatalf("list keys: %+v err=%v", keys, err)
	}

	// Resolve the hash → consumer identity.
	id, ok, err := db.ResolveAPIKey(ctx, hash)
	if err != nil || !ok || id.ConsumerID != cid {
		t.Fatalf("resolve key: id=%+v ok=%v err=%v", id, ok, err)
	}
	// Unknown hash does not resolve.
	if _, ok, _ := db.ResolveAPIKey(ctx, HashAPIKey("nope")); ok {
		t.Fatal("unknown key resolved")
	}
	// Revoked key does not resolve.
	if ok, err := db.RevokeAPIKey(ctx, keyID); err != nil || !ok {
		t.Fatalf("revoke: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := db.ResolveAPIKey(ctx, hash); ok {
		t.Fatal("revoked key still resolves")
	}
}

func TestResolveAPIKeyIncludesPlanLimit(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	pid, _ := db.UpsertPlan(ctx, model.Plan{Name: "pro", RPS: 500, Burst: 1000})
	cid, _ := db.UpsertConsumer(ctx, model.Consumer{Name: "acme", PlanID: pid, Enabled: true})
	if _, err := db.CreateAPIKey(ctx, cid, "prod", HashAPIKey("k")); err != nil {
		t.Fatal(err)
	}
	id, ok, _ := db.ResolveAPIKey(ctx, HashAPIKey("k"))
	if !ok || id.PlanID != pid || id.Limit.RPS != 500 || id.Limit.Burst != 1000 || !id.Limit.Enabled() {
		t.Fatalf("identity plan limit wrong: %+v", id)
	}

	// A consumer with no plan resolves with a disabled limit.
	cid2, _ := db.UpsertConsumer(ctx, model.Consumer{Name: "noplan", Enabled: true})
	if _, err := db.CreateAPIKey(ctx, cid2, "p", HashAPIKey("k2")); err != nil {
		t.Fatal(err)
	}
	id2, _, _ := db.ResolveAPIKey(ctx, HashAPIKey("k2"))
	if id2.Limit.Enabled() {
		t.Fatalf("no-plan consumer should have a disabled limit: %+v", id2)
	}
}

func TestResolveIgnoresDisabledConsumer(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	cid, _ := db.UpsertConsumer(ctx, model.Consumer{Name: "acme", Enabled: false})
	hash := HashAPIKey("k")
	if _, err := db.CreateAPIKey(ctx, cid, "prod", hash); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := db.ResolveAPIKey(ctx, hash); ok {
		t.Fatal("key of a disabled consumer should not resolve")
	}
}

func TestHashAPIKeyIsStableAndHidesSecret(t *testing.T) {
	h1 := HashAPIKey("abc")
	h2 := HashAPIKey("abc")
	if h1 != h2 {
		t.Fatal("hash not stable")
	}
	if h1 == "abc" || len(h1) != 64 { // sha-256 hex
		t.Fatalf("hash looks wrong: %q", h1)
	}
}

func TestConsumerWritesBumpVersion(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()
	start, _ := db.Version(ctx)

	pid, _ := db.UpsertPlan(ctx, model.Plan{Name: "free", RPS: 60, Burst: 60})
	cid, _ := db.UpsertConsumer(ctx, model.Consumer{Name: "acme", PlanID: pid, Enabled: true})
	_, _ = db.CreateAPIKey(ctx, cid, "prod", HashAPIKey("k"))

	end, _ := db.Version(ctx)
	if end != start+3 {
		t.Fatalf("version bumped %d times, want 3", end-start)
	}
}

func TestAdminUserCRUDAndSeed(t *testing.T) {
	db := openTemp(t)
	ctx := context.Background()

	// Seed once.
	seeded, err := SeedAdminUser(ctx, db, model.AdminUser{Username: "root", PasswordHash: "bcrypt$1", TokenVersion: 1})
	if err != nil || !seeded {
		t.Fatalf("seed admin: seeded=%v err=%v", seeded, err)
	}
	// Second seed is a no-op (an admin already exists).
	seeded, err = SeedAdminUser(ctx, db, model.AdminUser{Username: "other", PasswordHash: "x"})
	if err != nil || seeded {
		t.Fatalf("second seed should be no-op: seeded=%v err=%v", seeded, err)
	}

	u, ok, err := db.GetAdminUser(ctx, "root")
	if err != nil || !ok || u.PasswordHash != "bcrypt$1" || u.TokenVersion != 1 {
		t.Fatalf("get admin: %+v ok=%v err=%v", u, ok, err)
	}
	if _, ok, _ := db.GetAdminUser(ctx, "ghost"); ok {
		t.Fatal("missing admin resolved")
	}

	// Admin-user writes must NOT bump the data-plane config version.
	before, _ := db.Version(ctx)
	u.TokenVersion = 2
	if _, err := db.UpsertAdminUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	after, _ := db.Version(ctx)
	if after != before {
		t.Fatalf("admin write bumped config_version (%d->%d); it should not", before, after)
	}
}
