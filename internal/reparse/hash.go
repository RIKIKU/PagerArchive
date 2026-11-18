package reparse

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/RIKIKU/PagerArchive/internal/geocode"
	"github.com/RIKIKU/PagerArchive/internal/profile"
)

// ProfileHashes identifies the profiles and address dataset in use, so a
// change re-parses every page (section 14, R23a). hash leaves out what
// changes nothing a page says: the network's areas (R88d) and an agency's
// labels, levels, description and comments (ADR 0045). legacy is the hash
// as releases before ADR 0045 worked it out, from each agency's whole file:
// an install that saved it, with profiles unchanged since, isn't re-parsed
// by the upgrade (CheckProfiles).
func ProfileHashes(set *profile.Set, net *profile.Network) (hash, legacy string, err error) {
	dataset, err := geocode.DatasetHash(net.DatasetPath, net.StreetTypes)
	if err != nil {
		return "", "", fmt.Errorf("address dataset: %w", err)
	}
	names := make([]string, 0, len(set.Agencies))
	for n := range set.Agencies {
		names = append(names, n)
	}
	sort.Strings(names)
	sum := func(agency func(*profile.Agency) string) string {
		h := sha256.New()
		fmt.Fprintf(h, "network %s %s\n", net.Name, net.Hash)
		for _, n := range names {
			fmt.Fprintf(h, "agency %s %s\n", n, agency(set.Agencies[n]))
		}
		fmt.Fprintf(h, "dataset %s\n", dataset)
		return hex.EncodeToString(h.Sum(nil))
	}
	return sum(func(a *profile.Agency) string { return a.Hash }), sum(func(a *profile.Agency) string { return a.FileHash }), nil
}
