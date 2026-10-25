package certscripts_test

import (
	"errors"
	"os"
	"os/user"
	"strconv"
	"strings"
	"testing"

	"blacksmith/internal/upgrade/certscripts"
)

// refusedCert discovers the tree and returns the reason on the
// certificate's match, failing the test when it is accepted.
func refusedCert(t *testing.T, tree *vmTree, old pair) (*certscripts.Discovery, *certscripts.Refusal) {
	t.Helper()

	found := tree.discover(t, old.certHash(), old.keyHash())

	for i := range found.Matches {
		match := &found.Matches[i]
		if match.Path != valkeyCert {
			continue
		}

		if match.RootDirs {
			t.Fatalf("%s reports rootdirs=yes, want a refusal", match.Path)
		}

		if match.Why == nil {
			t.Fatalf("%s is refused with no reason", match.Path)
		}

		return found, match.Why
	}

	t.Fatalf("no match for %s in %+v", valkeyCert, found.Matches)

	return nil, nil
}

// buildError runs BuildConsumers on the discovery and returns its error.
func buildError(t *testing.T, found *certscripts.Discovery, old pair) string {
	t.Helper()

	found.VM = testVM

	_, err := certscripts.BuildConsumers(found,
		map[string]string{testVariable: old.certHash()},
		map[string]string{testVariable: old.keyHash()})
	if !errors.Is(err, certscripts.ErrPatchUnsafe) {
		t.Fatalf("BuildConsumers error = %v, want ErrPatchUnsafe", err)
	}

	return err.Error()
}

func wantAll(t *testing.T, text string, words ...string) {
	t.Helper()

	for _, word := range words {
		if !strings.Contains(text, word) {
			t.Errorf("message %q does not contain %q", text, word)
		}
	}

	if strings.Contains(text, "someone other than root can change") {
		t.Errorf("message %q still holds the catch-all", text)
	}
}

func groupName(t *testing.T, gid int) string {
	t.Helper()

	group, err := user.LookupGroupId(strconv.Itoa(gid))
	if err != nil {
		t.Skipf("no name for gid %d: %v", gid, err)
	}

	return group.Name
}

func TestDiscoverNamesGroupWritableDirectory(t *testing.T) {
	t.Parallel()

	old := newPair(t, valkeyJob)
	tree := newDiscoverTree(t, old)
	group := groupName(t, nobodyUID)

	err := os.Chown(tree.at(valkeyTLS), 0, nobodyUID)
	if err != nil {
		t.Fatal(err)
	}

	tree.chmod(t, valkeyTLS, 0o775)

	found, why := refusedCert(t, tree, old)

	want := certscripts.Refusal{Token: "dir-writable", At: valkeyTLS, Owner: "root:" + group, Mode: "0775"}
	if *why != want {
		t.Fatalf("reason = %+v, want %+v", *why, want)
	}

	wantAll(t, buildError(t, found, old), testVM, valkeyCert, testVariable,
		"the directory "+valkeyTLS+" can be changed by group "+group+" (owner root:"+group+", mode 0775)",
		"Redeploy it instead")
}

func TestDiscoverNamesWorldWritableDirectory(t *testing.T) {
	t.Parallel()

	old := newPair(t, valkeyJob)
	tree := newDiscoverTree(t, old)
	tree.chmod(t, valkeyTLS, 0o777)

	found, why := refusedCert(t, tree, old)

	if why.Token != "dir-writable" || why.At != valkeyTLS || why.Mode != "0777" || !strings.HasPrefix(why.Owner, "root:") {
		t.Fatalf("reason = %+v, want dir-writable on %s with mode 0777", *why, valkeyTLS)
	}

	wantAll(t, buildError(t, found, old), "can be changed by group", "and any user", "mode 0777")
}

func TestDiscoverNamesDirectoryNotOwnedByRoot(t *testing.T) {
	t.Parallel()

	old := newPair(t, valkeyJob)
	tree := newDiscoverTree(t, old)

	err := os.Chown(tree.at(valkeyJobDir), nobodyUID, 0)
	if err != nil {
		t.Fatal(err)
	}

	found, why := refusedCert(t, tree, old)

	if why.Token != "dir-owner" || why.At != valkeyJobDir || why.Mode != "0755" || !strings.HasPrefix(why.Owner, "nobody:") {
		t.Fatalf("reason = %+v, want dir-owner on %s with mode 0755", *why, valkeyJobDir)
	}

	wantAll(t, buildError(t, found, old), testVM, valkeyCert, testVariable,
		"the directory "+valkeyJobDir+" is owned by "+why.Owner+" rather than root (mode 0755)")
}

func TestDiscoverNamesSymlinkedDirectory(t *testing.T) {
	t.Parallel()

	old := newPair(t, valkeyJob)
	tree := newDiscoverTree(t, old)

	err := os.Rename(tree.at("/var/vcap/data"), tree.at("/srv-data"))
	if err != nil {
		t.Fatal(err)
	}

	symlink(t, tree.at("/srv-data"), tree.at("/var/vcap/data"))

	found, why := refusedCert(t, tree, old)

	if why.Token != "dir-symlink" || why.At != "/var/vcap/data" {
		t.Fatalf("reason = %+v, want dir-symlink on /var/vcap/data", *why)
	}

	wantAll(t, buildError(t, found, old), "the directory /var/vcap/data is a symbolic link")
}

func TestDiscoverNamesSecondLink(t *testing.T) {
	t.Parallel()

	old := newPair(t, valkeyJob)
	tree := newDiscoverTree(t, old)

	err := os.Link(tree.at(valkeyCert), tree.at(valkeyJobDir+"/config/copy.crt"))
	if err != nil {
		t.Fatal(err)
	}

	found, why := refusedCert(t, tree, old)

	if why.Token != "many-links" || why.At != valkeyCert {
		t.Fatalf("reason = %+v, want many-links on %s", *why, valkeyCert)
	}

	wantAll(t, buildError(t, found, old), "that file has more than one link")
}

// TestRefusalMessages covers the reasons discover.sh can only print when
// a file changes under it, so no fixture tree reaches them.
func TestRefusalMessages(t *testing.T) {
	t.Parallel()

	rows := []struct {
		name string
		line string
		want string
	}{
		{"job-link", " why=job-link at=/var/vcap/jobs/valkey", "the link /var/vcap/jobs/valkey is missing or does not point"},
		{"dir-missing", " why=dir-missing at=" + valkeyTLS, "the directory " + valkeyTLS + " is missing or is not a directory"},
		{"not-regular", " why=not-regular at=" + valkeyCert, "that file is not a plain file"},
		{"realpath", " why=realpath at=" + valkeyCert, "that file resolves to a different real path"},
		{"shape", " why=shape", "is not where a job's files belong"},
		{"other", " why=other", "a safety check failed that the scripts could not name"},
	}

	jobdirs := "jobdir job=valkey path=" + valkeyJobDir + "\n"

	for _, row := range rows {
		text := jobdirs + "match job=valkey path=" + valkeyCert + " hash=" + hashA + edges +
			" owner=root:root mode=0644 rootdirs=no" + row.line + "\n"

		_, err := consumersFor(t, text, map[string]string{testVariable: hashA}, map[string]string{testVariable: hashB})
		if err == nil {
			t.Errorf("%s: no error", row.name)

			continue
		}

		wantAll(t, err.Error(), testVM, valkeyCert, testVariable, row.want, "Redeploy it instead")
	}
}

func TestParseDiscoveryRejectsBadReasons(t *testing.T) {
	t.Parallel()

	good := discoveryText(hashA, hashB)
	line := func(rest string) string {
		return "match job=valkey path=" + valkeyCert + " hash=" + hashA + edges + " owner=root:root mode=0644 " + rest + "\n"
	}

	bad := map[string]string{
		"an unknown token":                  line("rootdirs=no why=because"),
		"no reason on rootdirs=no":          line("rootdirs=no"),
		"a reason on rootdirs=yes":          line("rootdirs=yes why=many-links at=" + valkeyCert),
		"a path on rootdirs=yes":            line("rootdirs=yes at=" + valkeyCert),
		"a directory token with no path":    line("rootdirs=no why=dir-writable by=root:vcap bits=0775"),
		"a directory token with no owner":   line("rootdirs=no why=dir-writable at=" + valkeyTLS + " bits=0775"),
		"a bad mode":                        line("rootdirs=no why=dir-writable at=" + valkeyTLS + " by=root:vcap bits=775"),
		"a bad owner":                       line("rootdirs=no why=dir-owner at=" + valkeyTLS + " by=vcap bits=0755"),
		"an owner on a token without one":   line("rootdirs=no why=many-links at=" + valkeyCert + " by=root:vcap bits=0775"),
		"a path that is not safe to print":  line("rootdirs=no why=dir-owner at=! by=root:vcap bits=0755"),
		"a path on a token that names none": line("rootdirs=no why=other at=" + valkeyCert),
	}

	for name, extra := range bad {
		found, err := certscripts.ParseDiscovery([]byte(good + extra))
		if err == nil {
			t.Errorf("%s: ParseDiscovery accepted %q", name, extra)

			continue
		}

		if found != nil {
			t.Errorf("%s: ParseDiscovery returned a result with its error", name)
		}

		if !errors.Is(err, certscripts.ErrMalformedOutput) {
			t.Errorf("%s: error %v is not ErrMalformedOutput", name, err)
		}
	}
}
