package carddav

import (
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/emersion/go-vcard"
	"github.com/migadu/go-webdav/internal"
)

func filterProperties(req AddressDataRequest, ao AddressObject) AddressObject {
	if req.AllProp || len(req.Props) == 0 || len(ao.Card) == 0 {
		return ao
	}

	result := ao
	result.Card = make(vcard.Card)
	// result would be invalid w/o version. It gets its own slice, so that
	// appending to it can't change ao.
	version := slices.Clone(lookupFold(ao.Card, vcard.FieldVersion))
	if len(version) > 0 {
		result.Card[vcard.FieldVersion] = version
	}
	// The fields matched by a name which isn't in req.NoValue, if it's set
	var valued map[*vcard.Field]bool
	if len(req.NoValue) > 0 {
		valued = make(map[*vcard.Field]bool)
		for _, field := range version {
			valued[field] = true
		}
	}
	for _, prop := range req.Props {
		_, name, _ := splitGroup(prop)
		key := strings.ToUpper(name)
		noValue := containsFold(req.NoValue, prop)
		for _, field := range cardFields(ao.Card, prop) {
			if valued != nil && !noValue {
				valued[field] = true
			}
			// The same field may be requested twice, such as with
			// and without its group
			if !slices.Contains(result.Card[key], field) {
				result.Card[key] = append(result.Card[key], field)
			}
		}
	}

	if valued != nil {
		for _, fields := range result.Card {
			for i, field := range fields {
				if !valued[field] {
					// Copy the field rather than change ao. The
					// encoder writes the parameters followed by
					// just a ":" (RFC 6352 section 10.4.2).
					f := *field
					f.Value = ""
					fields[i] = &f
				}
			}
		}
	}

	return result
}

// Filter returns the filtered list of address objects matching the provided query.
// A nil query will return the full list of address objects.
func Filter(query *AddressBookQuery, aos []AddressObject) ([]AddressObject, error) {
	if query == nil {
		// FIXME: should we always return a copy of the provided slice?
		return aos, nil
	}

	n := query.Limit
	if n <= 0 || n > len(aos) {
		n = len(aos)
	}
	out := make([]AddressObject, 0, n)
	for _, ao := range aos {
		ok, err := Match(query, &ao)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}

		out = append(out, filterProperties(query.DataRequest, ao))
		if len(out) >= n {
			break
		}
	}
	return out, nil
}

// Match reports whether the provided AddressObject matches the query.
func Match(query *AddressBookQuery, ao *AddressObject) (matched bool, err error) {
	if query == nil {
		return true, nil
	}

	switch query.FilterTest {
	default:
		return false, fmt.Errorf("unknown query filter test %q", query.FilterTest)

	case FilterAnyOf, "":
		if len(query.PropFilters) == 0 {
			return true, nil
		}
		for _, prop := range query.PropFilters {
			ok, err := matchPropFilter(prop, ao)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil

	case FilterAllOf:
		for _, prop := range query.PropFilters {
			ok, err := matchPropFilter(prop, ao)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}
		}
		return true, nil
	}
}

// splitGroup splits a CardDAV property name into its vCard group prefix, if
// any, and the vCard property name. Neither contains a dot (RFC 6350 section
// 3.3). ok is false if the name has a dot with nothing before or after it,
// such as ".TEL" or "item1.": groups and names aren't empty, so it names no
// property.
func splitGroup(name string) (group, prop string, ok bool) {
	if group, prop, found := strings.Cut(name, "."); found {
		return group, prop, group != "" && prop != ""
	}
	return "", name, true
}

// cardFields returns the fields of card named by name, a CardDAV property
// name (RFC 6352 sections 10.4.2 and 10.5.1): without a group prefix, it
// names the properties with any group prefix or none; with a group prefix,
// only the properties with that group prefix. Names are case-insensitive.
func cardFields(card vcard.Card, name string) []*vcard.Field {
	group, name, ok := splitGroup(name)
	if !ok {
		return nil
	}
	fields := lookupFold(card, name)
	if group == "" {
		return fields
	}
	var out []*vcard.Field
	for _, field := range fields {
		if strings.EqualFold(field.Group, group) {
			out = append(out, field)
		}
	}
	return out
}

// lookupFold returns the values of m under the keys equal to name, ignoring
// case. It returns the values of a single key as is: callers mustn't modify
// them.
//
// vcard.Decoder uppercases property and parameter names, but a vcard.Card or
// vcard.Params built in Go may have several keys differing only by case. All
// of them have to be looked at, so they're merged in key order, which,
// unlike map iteration order, is always the same.
func lookupFold[T any](m map[string][]T, name string) []T {
	var key string
	n := 0
	for k := range m {
		if strings.EqualFold(k, name) {
			key = k
			n++
		}
	}
	switch n {
	case 0:
		return nil
	case 1:
		return m[key]
	}

	keys := make([]string, 0, n)
	for k := range m {
		if strings.EqualFold(k, name) {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var out []T
	for _, k := range keys {
		out = append(out, m[k]...)
	}
	return out
}

// containsFold reports whether list holds s, ignoring case.
func containsFold(list []string, s string) bool {
	return slices.ContainsFunc(list, func(v string) bool {
		return strings.EqualFold(v, s)
	})
}

func matchPropFilter(prop PropFilter, ao *AddressObject) (bool, error) {
	switch prop.Test {
	case FilterAnyOf, "", FilterAllOf:
	default:
		return false, fmt.Errorf("unknown property filter test %q", prop.Test)
	}

	fields := cardFields(ao.Card, prop.Name)
	if len(fields) == 0 {
		return prop.IsNotDefined, nil
	}
	if prop.IsNotDefined {
		return false, nil
	}

	if len(prop.TextMatches) == 0 && len(prop.Params) == 0 {
		return true, nil
	}

	for _, field := range fields {
		ok, err := matchFieldPropFilter(prop, field)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func matchFieldPropFilter(prop PropFilter, field *vcard.Field) (bool, error) {
	switch prop.Test {
	case FilterAnyOf, "":
		for _, param := range prop.Params {
			ok, err := matchParamFilter(param, field)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		for _, txt := range prop.TextMatches {
			ok, err := matchTextMatch(txt, field)
			if err != nil {
				return false, err
			}
			if ok {
				return true, nil
			}
		}
		return false, nil

	case FilterAllOf:
		for _, param := range prop.Params {
			ok, err := matchParamFilter(param, field)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}
		}
		for _, txt := range prop.TextMatches {
			ok, err := matchTextMatch(txt, field)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, nil
			}
		}
		return true, nil

	default:
		return false, fmt.Errorf("unknown property filter test %q", prop.Test)
	}
}

func matchParamFilter(param ParamFilter, field *vcard.Field) (bool, error) {
	values := lookupFold(field.Params, param.Name)
	if len(values) == 0 {
		return param.IsNotDefined, nil
	}
	if param.IsNotDefined {
		return false, nil
	}
	if param.TextMatch == nil {
		return true, nil
	}

	tm := *param.TextMatch
	tm.NegateCondition = false
	matched := false
	for _, v := range values {
		m, err := matchTextMatchValue(tm, v)
		if err != nil {
			return false, err
		}
		if m {
			matched = true
			break
		}
	}
	if param.TextMatch.NegateCondition {
		matched = !matched
	}
	return matched, nil
}

func matchTextMatch(txt TextMatch, field *vcard.Field) (bool, error) {
	return matchTextMatchValue(txt, field.Value)
}

func matchTextMatchValue(txt TextMatch, value string) (bool, error) {
	valKey, supported := internal.CollateKey(value, txt.Collation, internal.CollationUnicodeCasemap)
	if !supported {
		return false, newPreconditionError(http.StatusForbidden, PreconditionSupportedCollation)
	}
	subKey, _ := internal.CollateKey(txt.Text, txt.Collation, internal.CollationUnicodeCasemap)

	var ok bool
	switch txt.MatchType {
	default:
		return false, fmt.Errorf("unknown textmatch type %q", txt.MatchType)

	case MatchEquals:
		ok = subKey == valKey

	case MatchContains, "":
		ok = strings.Contains(valKey, subKey)

	case MatchStartsWith:
		ok = strings.HasPrefix(valKey, subKey)

	case MatchEndsWith:
		ok = strings.HasSuffix(valKey, subKey)
	}

	if txt.NegateCondition {
		ok = !ok
	}
	return ok, nil
}
