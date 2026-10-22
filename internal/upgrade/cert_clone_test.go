//nolint:testpackage // the tests copy tasks through the unexported copyTask
package upgrade

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"
)

// cloneSamples holds one value of each type that has a Clone method, as
// the type its Clone is called on.
func cloneSamples() []any {
	return []any{
		&CertificateTaskSpec{},
		&InstanceRotation{},
		ApprovedEffects{},
		VariableRotation{},
		&PendingDecision{},
		Candidate{},
		VMJournal{},
		FileEntry{},
		&ConvergeDeploy{},
	}
}

// fillValue sets every exported field the value reaches to something that
// is not its zero value. A slice gets one element, a map one entry, and a
// pointer a fresh target, each filled in turn, so a copy that shares any of
// them can be found.
func fillValue(t *testing.T, value reflect.Value) {
	t.Helper()

	//nolint:exhaustive // a task holds no other kinds, and default fails on one
	switch value.Kind() {
	case reflect.Pointer:
		target := reflect.New(value.Type().Elem())
		fillValue(t, target.Elem())
		value.Set(target)
	case reflect.Struct:
		if value.Type() == reflect.TypeFor[time.Time]() {
			value.Set(reflect.ValueOf(time.Date(2026, 10, 22, 8, 30, 0, 0, time.UTC)))

			return
		}

		for i := range value.NumField() {
			if value.Type().Field(i).IsExported() {
				fillValue(t, value.Field(i))
			}
		}
	case reflect.Slice:
		slice := reflect.MakeSlice(value.Type(), 1, 1)
		fillValue(t, slice.Index(0))
		value.Set(slice)
	case reflect.Map:
		key := reflect.New(value.Type().Key()).Elem()
		fillValue(t, key)

		element := reflect.New(value.Type().Elem()).Elem()
		fillValue(t, element)

		filled := reflect.MakeMap(value.Type())
		filled.SetMapIndex(key, element)
		value.Set(filled)
	case reflect.String:
		value.SetString("filled")
	case reflect.Bool:
		value.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value.SetInt(7)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value.SetUint(7)
	case reflect.Float32, reflect.Float64:
		value.SetFloat(7)
	default:
		t.Fatalf("fillValue does not know how to fill a %s", value.Type())
	}
}

// expectDisjoint fails the test where the copy shares a slice's backing
// array, a map, or a pointer's target with the original, at any depth.
func expectDisjoint(t *testing.T, path string, original, copied reflect.Value) {
	t.Helper()

	//nolint:exhaustive // every other kind is a value, which a copy never shares
	switch original.Kind() {
	case reflect.Pointer:
		expectDisjointPointer(t, path, original, copied)
	case reflect.Struct:
		if original.Type() == reflect.TypeFor[time.Time]() {
			return
		}

		for i := range original.NumField() {
			field := original.Type().Field(i)
			if field.IsExported() {
				expectDisjoint(t, path+"."+field.Name, original.Field(i), copied.Field(i))
			}
		}
	case reflect.Slice:
		expectDisjointSlice(t, path, original, copied)
	case reflect.Map:
		expectDisjointMap(t, path, original, copied)
	default:
	}
}

// expectDisjointPointer checks a pointer for expectDisjoint.
func expectDisjointPointer(t *testing.T, path string, original, copied reflect.Value) {
	t.Helper()

	if original.IsNil() || copied.IsNil() {
		return
	}

	if original.Pointer() == copied.Pointer() {
		t.Errorf("%s: the copy shares the original's pointer", path)

		return
	}

	expectDisjoint(t, path, original.Elem(), copied.Elem())
}

// expectDisjointSlice checks a slice for expectDisjoint.
func expectDisjointSlice(t *testing.T, path string, original, copied reflect.Value) {
	t.Helper()

	if original.Len() == 0 || copied.Len() == 0 {
		return
	}

	if original.Pointer() == copied.Pointer() {
		t.Errorf("%s: the copy shares the original's slice", path)

		return
	}

	for i := range min(original.Len(), copied.Len()) {
		expectDisjoint(t, path+"[]", original.Index(i), copied.Index(i))
	}
}

// expectDisjointMap checks a map for expectDisjoint.
func expectDisjointMap(t *testing.T, path string, original, copied reflect.Value) {
	t.Helper()

	if original.IsNil() || copied.IsNil() {
		return
	}

	if original.Pointer() == copied.Pointer() {
		t.Errorf("%s: the copy shares the original's map", path)

		return
	}

	for _, key := range original.MapKeys() {
		if value := copied.MapIndex(key); value.IsValid() {
			expectDisjoint(t, path+"{}", original.MapIndex(key), value)
		}
	}
}

// cloneOf calls the value's Clone method through reflection.
func cloneOf(t *testing.T, value reflect.Value) reflect.Value {
	t.Helper()

	method := value.MethodByName("Clone")
	if !method.IsValid() {
		t.Fatalf("%s has no Clone method", value.Type())
	}

	return method.Call(nil)[0]
}

// Every type with a Clone method copies every slice, map, and pointer it
// reaches, so a field added later without a copy fails here.
func TestCloneCoversEveryField(t *testing.T) {
	t.Parallel()

	for _, sample := range cloneSamples() {
		original := reflect.New(reflect.TypeOf(sample)).Elem()
		fillValue(t, original)

		copied := cloneOf(t, original)

		if !reflect.DeepEqual(original.Interface(), copied.Interface()) {
			t.Errorf("%s: the clone differs from the original", original.Type())
		}

		expectDisjoint(t, original.Type().String(), original, copied)
	}
}

// A nil pointer clones to nil, so an absent rotation stays absent.
func TestCloneOfNilIsNil(t *testing.T) {
	t.Parallel()

	var (
		spec     *CertificateTaskSpec
		rotation *InstanceRotation
		decision *PendingDecision
		converge *ConvergeDeploy
	)

	if spec.Clone() != nil || rotation.Clone() != nil || decision.Clone() != nil || converge.Clone() != nil {
		t.Fatalf("expected the clone of a nil pointer to be nil")
	}
}

// Every type that declares a Clone method in the package is listed in
// cloneSamples, so the field guard above checks it.
func TestCloneSamplesNameEveryCloneMethod(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("listing the package's files: %v", err)
	}

	listed := make(map[string]bool)

	for _, sample := range cloneSamples() {
		kind := reflect.TypeOf(sample)
		if kind.Kind() == reflect.Pointer {
			kind = kind.Elem()
		}

		listed[kind.Name()] = true
	}

	fileSet := token.NewFileSet()

	for _, file := range files {
		parsed, err := parser.ParseFile(fileSet, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", file, err)
		}

		for _, decl := range parsed.Decls {
			function, ok := decl.(*ast.FuncDecl)
			if !ok || function.Recv == nil || function.Name.Name != "Clone" {
				continue
			}

			receiver := function.Recv.List[0].Type
			if star, ok := receiver.(*ast.StarExpr); ok {
				receiver = star.X
			}

			name := receiver.(*ast.Ident).Name //nolint:forcetypeassert // every receiver here is a named type
			if !listed[name] {
				t.Errorf("%s has a Clone method in %s but is missing from cloneSamples", name, file)
			}
		}
	}
}

// copyTask shares nothing with the task it copies, at any depth.
func TestCopyTaskSharesNothing(t *testing.T) {
	t.Parallel()

	original := reflect.New(reflect.TypeFor[UpgradeTask]()).Elem()
	fillValue(t, original)

	task := original.Addr().Interface().(*UpgradeTask) //nolint:forcetypeassert // built from that type
	copied := copyTask(task)

	if !reflect.DeepEqual(task, copied) {
		t.Fatalf("expected the copy to equal the task")
	}

	expectDisjoint(t, "UpgradeTask", reflect.ValueOf(task).Elem(), reflect.ValueOf(copied).Elem())
}

// A change to any part of a copied certificate task never reaches the
// original.
func TestCopyTaskDeepCopiesCertificates(t *testing.T) {
	t.Parallel()

	original := fullCertificateTask()
	before := fullCertificateTask()

	copied := copyTask(&original)
	rotation := copied.Instances[0].Certificates

	const changed = "changed"

	rotation.Approved.Selected[0].LatestVersionID = changed
	rotation.Approved.Additional[0].Name = changed
	rotation.Approved.VMCIDs[0] = changed
	rotation.Pending.Candidates[0].Checks[0].Passed = false
	rotation.Pending.Candidates[0].VersionID = changed
	rotation.Pending.Changes[0] = changed
	rotation.VMs[0].Files[0].State = FileStateReplaced
	rotation.VMs[0].JobDirs[certJob] = changed
	rotation.VMs[0].Targets[0].OK = false
	rotation.VMs[0].Processes[0] = changed
	rotation.Variables[0].Validation[0].Passed = false
	rotation.Variables[0].Baseline[0] = changed
	*rotation.Variables[0].NewNotAfter = time.Time{}
	rotation.Converge.BOSHTaskID = 0
	rotation.Phase = RotationPhaseDone
	copied.Certificates.Mode = RotationModeConverge
	copied.Instances[0].Certificates = nil

	if !reflect.DeepEqual(original, before) {
		t.Fatalf("a change to the copy reached the original")
	}

	if !slices.Equal(original.Instances[0].Certificates.Approved.VMCIDs, before.Instances[0].Certificates.Approved.VMCIDs) {
		t.Fatalf("a change to the copy's VM CIDs reached the original")
	}
}
