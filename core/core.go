package core

import (
	"os/exec"
	"sort"
	"sync"

	"github.com/evilsocket/islazy/str"
)

var (
	preLookupTable = make(map[string]bool)   // used to indicate if we have the executable already looked up
	preLookupPaths = make(map[string]string) // used to hold the paths for executables that we already have looked up
	preLookupLock  sync.RWMutex
)

// PopulatePreLookupTable is used to pre-populate a list of
// commonly used executables along with their executable paths
// to avoid spending time repeatedly doing this
func PopulatePreLookupTable(lookups ...string) {
	for _, lookup := range lookups {
		if HasBinary(lookup) {
			preLookupLock.Lock()
			preLookupTable[lookup] = true
			preLookupLock.Unlock()
			path, err := exec.LookPath(lookup)
			if err != nil {
				panic("HasBinary returned true but LookPath failed, this is unexpected")
			}
			if path != "" {
				preLookupLock.Lock()
				preLookupPaths[lookup] = path
				preLookupLock.Unlock()
			}
		} else {
			continue
		}
	}
}

func UniqueInts(a []int, sorted bool) []int {
	tmp := make(map[int]bool, len(a))
	uniq := make([]int, 0, len(a))

	for _, n := range a {
		tmp[n] = true
	}

	for n := range tmp {
		uniq = append(uniq, n)
	}

	if sorted {
		sort.Ints(uniq)
	}

	return uniq
}

func HasBinary(executable string) bool {
	// first check lookup table
	preLookupLock.RLock()
	has := preLookupTable[executable]
	preLookupLock.RUnlock()
	if has {
		return true
	}
	// ok we dont have this in the prelookup table so take the slow-route
	// of looking it up on disk
	if path, err := exec.LookPath(executable); err != nil || path == "" {
		return false
	}
	return true
}

func Exec(executable string, args []string) (string, error) {
	var (
		path string
		err  error
	)
	preLookupLock.RLock()
	path = preLookupPaths[executable]
	preLookupLock.RUnlock()
	// we dont have this in the pre-lookup table
	// so take the slow route of looking it up on disk
	if path == "" {
		path, err = exec.LookPath(executable)
		if err != nil {
			return "", err
		}
	}

	raw, err := exec.Command(path, args...).CombinedOutput()
	if err != nil {
		return "", err
	} else {
		return str.Trim(string(raw)), nil
	}
}
