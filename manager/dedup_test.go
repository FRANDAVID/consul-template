package manager

import (
	"io/ioutil"
	"testing"
	"time"

	"github.com/hashicorp/consul-template/config"
	"github.com/hashicorp/consul-template/dependency"
	"github.com/hashicorp/consul-template/template"
	"github.com/hashicorp/consul/testutil"
)

func testConsulServer(t *testing.T) *testutil.TestServer {
	return testutil.NewTestServerConfig(t, func(c *testutil.TestServerConfig) {
		c.Stdout = ioutil.Discard
		c.Stderr = ioutil.Discard
	})
}

func testDedupManager(t *testing.T, addr string, tmpls []*template.Template) *DedupManager {
	// Setup the configuration
	c := config.TestConfig(&config.Config{
		Consul: config.String(addr),
	})

	// Create the clientset
	clients, err := newClientSet(c)
	if err != nil {
		t.Fatalf("runner: %s", err)
	}

	// Setup a brain
	brain := template.NewBrain()

	// Create the dedup manager
	dedup, err := NewDedupManager(c.Dedup, clients, brain, tmpls)
	if err != nil {
		t.Fatal(err)
	}
	return dedup
}

func TestDedup_StartStop(t *testing.T) {
	t.Parallel()

	consul := testConsulServer(t)
	defer consul.Stop()

	dedup := testDedupManager(t, consul.HTTPAddr, nil)

	// Start and stop
	if err := dedup.Start(); err != nil {
		t.Fatal(err)
	}
	if err := dedup.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestDedup_IsLeader(t *testing.T) {
	t.Parallel()

	// Create a template
	tmpl, err := template.NewTemplate(&template.NewTemplateInput{
		Contents: `{{ range service "consul" }}{{ .Node }}{{ end }}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	consul := testConsulServer(t)
	defer consul.Stop()

	dedup := testDedupManager(t, consul.HTTPAddr, []*template.Template{tmpl})
	if err := dedup.Start(); err != nil {
		t.Fatal(err)
	}
	defer dedup.Stop()

	// Wait until we are leader
	select {
	case <-dedup.UpdateCh():
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout")
	}

	// Check that we are the leader
	if !dedup.IsLeader(tmpl) {
		t.Fatalf("should be leader")
	}
}

func TestDedup_UpdateDeps(t *testing.T) {
	t.Parallel()

	// Create a template
	tmpl, err := template.NewTemplate(&template.NewTemplateInput{
		Contents: `{{ range service "consul" }}{{ .Node }}{{ end }}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	consul := testConsulServer(t)
	defer consul.Stop()

	dedup := testDedupManager(t, consul.HTTPAddr, []*template.Template{tmpl})
	if err := dedup.Start(); err != nil {
		t.Fatal(err)
	}
	defer dedup.Stop()

	// Wait until we are leader
	select {
	case <-dedup.UpdateCh():
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout")
	}

	// Create the dependency
	dep, err := dependency.ParseHealthServices("consul")
	if err != nil {
		t.Fatal(err)
	}

	// Inject data into the brain
	dedup.brain.Remember(dep, 123)

	// Update the dependencies
	err = dedup.UpdateDeps(tmpl, []dependency.Dependency{dep})
	if err != nil {
		t.Fatal(err)
	}
}

func TestDedup_FollowerUpdate(t *testing.T) {
	t.Parallel()

	// Create a template
	tmpl, err := template.NewTemplate(&template.NewTemplateInput{
		Contents: `{{ range service "consul" }}{{ .Node }}{{ end }}`,
	})
	if err != nil {
		t.Fatal(err)
	}

	consul := testConsulServer(t)
	defer consul.Stop()

	dedup1 := testDedupManager(t, consul.HTTPAddr, []*template.Template{tmpl})
	if err := dedup1.Start(); err != nil {
		t.Fatal(err)
	}
	defer dedup1.Stop()

	dedup2 := testDedupManager(t, consul.HTTPAddr, []*template.Template{tmpl})
	if err := dedup2.Start(); err != nil {
		t.Fatal(err)
	}
	defer dedup2.Stop()

	// Wait until we have a leader
	var leader, follow *DedupManager
	select {
	case <-dedup1.UpdateCh():
		if dedup1.IsLeader(tmpl) {
			leader = dedup1
			follow = dedup2
		}
	case <-dedup2.UpdateCh():
		if dedup2.IsLeader(tmpl) {
			leader = dedup2
			follow = dedup1
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout")
	}

	// Create the dependency
	dep, err := dependency.ParseHealthServices("consul")
	if err != nil {
		t.Fatal(err)
	}

	// Inject data into the brain
	leader.brain.Remember(dep, 123)

	// Update the dependencies
	err = leader.UpdateDeps(tmpl, []dependency.Dependency{dep})
	if err != nil {
		t.Fatal(err)
	}

	// Follower should get an update
	select {
	case <-follow.UpdateCh():
	case <-time.After(2 * time.Second):
		t.Fatalf("timeout")
	}

	// Recall from the brain
	data, ok := follow.brain.Recall(dep)
	if !ok {
		t.Fatalf("missing data")
	}
	if data != 123 {
		t.Fatalf("bad: %v", data)
	}
}
