// Package slot assigns each linked worktree a slot number. Slot N moves
// every port by N*10, so worktrees run next to the main checkout (slot 0).
package slot

import (
	"errors"
	"fmt"
	"io"
	"strconv"

	"github.com/gstark/devkit/internal/envfile"
	"github.com/gstark/devkit/internal/gitwt"
	"github.com/gstark/devkit/internal/manifest"
	"github.com/gstark/devkit/internal/ports"
	"github.com/gstark/devkit/internal/resolve"
)

// MaxSlot bounds the search. Slot 99 is port 3990 for a 3000 default.
const MaxSlot = 99

// ErrUnclaimed means a linked worktree has no slot yet.
var ErrUnclaimed = errors.New("this worktree has no slot yet; run `devkit setup` or `devkit slot claim`")

// Current returns the checkout's slot: 0 for the main checkout, else the
// value in its env file.
func Current(m *manifest.Manifest, wt gitwt.Info) (int, error) {
	if wt.IsMain() {
		return 0, nil
	}
	values, err := envfile.ReadAll(m.EnvFilePath(wt.Root))
	if err != nil {
		return 0, err
	}
	if slot, ok := fromEnv(m, values); ok {
		return slot, nil
	}
	return 0, ErrUnclaimed
}

// Claim gives a linked worktree the first free slot and writes it, with the
// named ports, to its env file. A slot already claimed is kept. A slot is
// free when no sibling worktree's env file holds it and nothing listens on
// any of its ports.
func Claim(m *manifest.Manifest, wt gitwt.Info, out io.Writer) (int, error) {
	if wt.IsMain() {
		return 0, errors.New("the main checkout is slot 0; claim runs in a linked worktree")
	}
	current, err := Current(m, wt)
	if err == nil {
		return current, nil
	}
	if !errors.Is(err, ErrUnclaimed) {
		return 0, err
	}

	used := map[int]bool{}
	for _, sibling := range wt.Siblings() {
		values, err := envfile.ReadAll(m.EnvFilePath(sibling))
		if err != nil {
			continue
		}
		if slot, ok := fromEnv(m, values); ok {
			used[slot] = true
		}
	}

	for slot := 1; slot <= MaxSlot; slot++ {
		if used[slot] || !Free(m, slot) {
			continue
		}
		if err := Write(m, wt, slot); err != nil {
			return 0, err
		}
		fmt.Fprintf(out, "Claimed slot %d.\n", slot)
		return slot, nil
	}
	return 0, fmt.Errorf("no free slot between 1 and %d", MaxSlot)
}

// Write records slot and its named ports in the worktree's env file.
func Write(m *manifest.Manifest, wt gitwt.Info, slot int) error {
	set := map[string]string{resolve.SlotKey: strconv.Itoa(slot)}
	for name, port := range m.Ports {
		set[port.Env] = strconv.Itoa(ports.ForSlot(port.Default, slot))
		_ = name
	}
	return envfile.Update(m.EnvFilePath(wt.Root), set, nil)
}

// Release removes the slot and port keys from the env file.
func Release(m *manifest.Manifest, wt gitwt.Info) error {
	keys := Keys(m)
	return envfile.Remove(m.EnvFilePath(wt.Root), keys...)
}

// Keys lists the env file keys slot management owns.
func Keys(m *manifest.Manifest) []string {
	keys := []string{resolve.SlotKey}
	for _, name := range m.PortNames() {
		keys = append(keys, m.Ports[name].Env)
	}
	return keys
}

// Free reports whether nothing listens on any port the slot would use: the
// named ports and every service's TCP and UDP range.
func Free(m *manifest.Manifest, slot int) bool {
	for _, port := range m.Ports {
		if !ports.TCPFree(ports.ForSlot(port.Default, slot)) {
			return false
		}
	}
	for _, name := range m.ServiceNames() {
		spec := m.Resolve(name, "dev")
		if !ports.RangeFree(ports.ForSlot(spec.DefaultPort, slot), spec.Ports.TCP, spec.Ports.UDP) {
			return false
		}
	}
	return true
}

// fromEnv reads the slot from an env file's values: DEVKIT_SLOT first, else
// derived from a named port that sits on a slot boundary.
func fromEnv(m *manifest.Manifest, values map[string]string) (int, bool) {
	if raw, ok := values[resolve.SlotKey]; ok {
		if slot, err := strconv.Atoi(raw); err == nil && slot >= 0 {
			return slot, true
		}
	}
	for _, name := range m.PortNames() {
		port := m.Ports[name]
		value, err := strconv.Atoi(values[port.Env])
		if err != nil {
			continue
		}
		delta := value - port.Default
		if delta > 0 && delta%ports.Spacing == 0 {
			return delta / ports.Spacing, true
		}
	}
	return 0, false
}
