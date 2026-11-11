package carddav

import (
	"fmt"
	"strings"

	"github.com/emersion/go-vcard"
)

func filterProperties(req AddressDataRequest, ao AddressObject) AddressObject {
	if req.AllProp || len(req.Props) == 0 || len(ao.Card) == 0 {
		return ao
	}

	result := ao
	result.Card = make(vcard.Card)
	// result would be invalid w/o version
	if version, ok := ao.Card[vcard.FieldVersion]; ok {
		result.Card[vcard.FieldVersion] = version
	}
	for _, prop := range req.Props {
		if value, ok := ao.Card[prop]; ok {
			result.Card[prop] = value
			continue
		}
		if fields := cardFields(ao.Card, prop); len(fields) > 0 {
			result.Card[strings.ToUpper(prop)] = fields
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

func cardFields(card vcard.Card, name string) []*vcard.Field {
	if len(card) == 0 {
		return nil
	}
	if fields, ok := card[name]; ok {
		return fields
	}
	if upper := strings.ToUpper(name); upper != name {
		if fields, ok := card[upper]; ok {
			return fields
		}
	}
	var out []*vcard.Field
	for k, fields := range card {
		if strings.EqualFold(k, name) {
			out = append(out, fields...)
		}
	}
	return out
}

func paramValues(params vcard.Params, name string) ([]string, bool) {
	if len(params) == 0 {
		return nil, false
	}
	if vals, ok := params[name]; ok {
		return vals, len(vals) > 0
	}
	if upper := strings.ToUpper(name); upper != name {
		if vals, ok := params[upper]; ok {
			return vals, len(vals) > 0
		}
	}
	var out []string
	var found bool
	for k, vals := range params {
		if strings.EqualFold(k, name) {
			out = append(out, vals...)
			found = found || len(vals) > 0
		}
	}
	return out, found
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
	values, ok := paramValues(field.Params, param.Name)
	if !ok {
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
	// TODO: handle text-match collation attribute
	var ok bool
	switch txt.MatchType {
	default:
		return false, fmt.Errorf("unknown textmatch type %q", txt.MatchType)

	case MatchEquals:
		ok = txt.Text == value

	case MatchContains, "":
		ok = strings.Contains(value, txt.Text)

	case MatchStartsWith:
		ok = strings.HasPrefix(value, txt.Text)

	case MatchEndsWith:
		ok = strings.HasSuffix(value, txt.Text)
	}

	if txt.NegateCondition {
		ok = !ok
	}
	return ok, nil
}
