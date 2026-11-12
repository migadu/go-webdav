package caldav

import (
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav/internal"
)

// Filter returns the filtered list of calendar objects matching the provided
// query, with only the parts of their data query.CompRequest asks for (see
// CalendarCompRequest). The objects of cos are left as they are. These parts
// may not make a valid iCalendar object, which ical.Encoder refuses to
// encode. A nil query will return the full list of calendar objects.
//
// If query.CompRequest also asks for Expand, LimitRecurrenceSet or
// LimitFreeBusySet, the objects are returned whole: the backend applies these
// to the objects Filter returns, which needs parts the client may not ask
// for, such as the RRULE of recurring components. The Handler writes only the
// requested parts of the objects the backend returns in any case.
func Filter(query *CalendarQuery, cos []CalendarObject) ([]CalendarObject, error) {
	if query == nil {
		// FIXME: should we always return a copy of the provided slice?
		return cos, nil
	}

	dataReq := &query.CompRequest
	if dataReq.Expand != nil || dataReq.LimitRecurrenceSet != nil || dataReq.LimitFreeBusySet != nil {
		dataReq = nil
	}
	var out []CalendarObject
	for _, co := range cos {
		ok, err := Match(query.CompFilter, &co)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}

		co.Data = filterCalendar(dataReq, co.Data)
		out = append(out, co)
	}
	return out, nil
}

// filterCalendar returns the parts of cal req asks for (RFC 4791 section 9.6,
// see CalendarCompRequest): cal itself if req is nil or asks for all of it.
// Otherwise the result is a new calendar, which shares with cal the
// components it holds whole, and cal is left as it is.
func filterCalendar(req *CalendarCompRequest, cal *ical.Calendar) *ical.Calendar {
	if req == nil || cal == nil || cal.Component == nil {
		return cal
	}
	// The outermost request is for the calendar object, whatever its name
	comp := filterComponent(req, cal.Component)
	if comp == cal.Component {
		return cal
	}
	return &ical.Calendar{Component: comp}
}

// filterComponent returns the parts of comp req asks for: comp itself if
// that's all of it, otherwise a new component. The caller checks that req is
// for comp.
func filterComponent(req *CalendarCompRequest, comp *ical.Component) *ical.Component {
	// An empty comp element asks for the whole component, as for VTIMEZONE
	// in the example of RFC 4791 section 7.8.1
	noProps := !req.AllProps && len(req.Props) == 0
	noComps := !req.AllComps && len(req.Comps) == 0
	if (noProps && noComps) || (req.AllProps && req.AllComps) {
		return comp
	}

	out := &ical.Component{Name: comp.Name, Props: make(ical.Props)}
	for name, props := range comp.Props {
		if !req.AllProps && !containsFold(req.Props, name) {
			continue
		}
		// Copy the slice, so that changing the result, starting with the
		// values of novalue properties, can't change comp
		props = slices.Clone(props)
		if !req.AllProps && containsFold(req.NoValue, name) {
			for i := range props {
				// The encoder writes the parameters followed by just
				// a ":" (RFC 4791 section 9.6.4)
				props[i].Value = ""
			}
		}
		out.Props[name] = props
	}

	if req.AllComps {
		out.Children = slices.Clone(comp.Children)
	} else {
		for _, child := range comp.Children {
			i := slices.IndexFunc(req.Comps, func(c CalendarCompRequest) bool {
				return strings.EqualFold(c.Name, child.Name)
			})
			if i >= 0 {
				out.Children = append(out.Children, filterComponent(&req.Comps[i], child))
			}
		}
	}
	return out
}

// containsFold reports whether list holds s, ignoring case.
func containsFold(list []string, s string) bool {
	return slices.ContainsFunc(list, func(v string) bool {
		return strings.EqualFold(v, s)
	})
}

// Match reports whether the provided CalendarObject matches the query.
func Match(query CompFilter, co *CalendarObject) (matched bool, err error) {
	if co.Data == nil || co.Data.Component == nil {
		panic("request to process empty calendar object")
	}
	return match(query, co.Data.Component)
}

func match(filter CompFilter, comp *ical.Component) (bool, error) {
	if !strings.EqualFold(comp.Name, filter.Name) {
		return filter.IsNotDefined, nil
	}
	if filter.IsNotDefined {
		return false, nil
	}

	if !filter.Start.IsZero() || !filter.End.IsZero() {
		match, err := matchCompTimeRange(filter.Start, filter.End, comp)
		if err != nil {
			return false, err
		}
		if !match {
			return false, nil
		}
	}
	for _, compFilter := range filter.Comps {
		match, err := matchCompFilter(compFilter, comp)
		if err != nil {
			return false, err
		}
		if !match {
			return false, nil
		}
	}
	for _, propFilter := range filter.Props {
		match, err := matchPropFilter(propFilter, comp)
		if err != nil {
			return false, err
		}
		if !match {
			return false, nil
		}
	}
	return true, nil
}

func matchCompFilter(filter CompFilter, comp *ical.Component) (bool, error) {
	if filter.IsNotDefined {
		for _, child := range comp.Children {
			if strings.EqualFold(child.Name, filter.Name) {
				return false, nil
			}
		}
		return true, nil
	}

	for _, child := range comp.Children {
		if !strings.EqualFold(child.Name, filter.Name) {
			continue
		}
		ok, err := match(filter, child)
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func matchPropFilter(filter PropFilter, comp *ical.Component) (bool, error) {
	fields := comp.Props.Values(filter.Name)
	if len(fields) == 0 {
		return filter.IsNotDefined, nil
	}
	if filter.IsNotDefined {
		return false, nil
	}

	for i := range fields {
		ok, err := matchProp(filter, &fields[i])
		if err != nil {
			return false, err
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

func matchProp(filter PropFilter, field *ical.Prop) (bool, error) {
	for _, paramFilter := range filter.ParamFilter {
		ok, err := matchParamFilter(paramFilter, field)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, nil
		}
	}

	if !filter.Start.IsZero() || !filter.End.IsZero() {
		return matchPropTimeRange(filter.Start, filter.End, field)
	}
	if filter.TextMatch != nil {
		return matchTextMatch(*filter.TextMatch, field.Value)
	}
	// empty prop-filter, property exists
	return true, nil
}

// matchCompTimeRange reports whether a component overlaps the time range
// [start, end), as defined in RFC 4791 section 9.9. A zero start or end leaves
// the range open on that side.
func matchCompTimeRange(start, end time.Time, comp *ical.Component) (bool, error) {
	rset, err := comp.RecurrenceSet(time.UTC)
	if err != nil {
		return false, err
	}

	// overlaps reports whether the occurrence starting at occStart overlaps
	// the range
	var overlaps func(occStart time.Time) bool
	switch comp.Name {
	case ical.CompEvent:
		event := ical.Event{Component: comp}
		eventStart, err := event.DateTimeStart(time.UTC)
		if err != nil {
			return false, err
		}
		eventEnd, err := event.DateTimeEnd(time.UTC)
		if err != nil {
			return false, err
		}
		if rset == nil {
			return overlapsTimeRange(start, end, eventStart, eventEnd), nil
		}
		duration := eventEnd.Sub(eventStart)
		overlaps = func(occStart time.Time) bool {
			return overlapsTimeRange(start, end, occStart, occStart.Add(duration))
		}
	case ical.CompToDo:
		todo, err := newTodoTimes(comp)
		if err != nil {
			return false, err
		}
		if rset == nil || todo.dtstart.IsZero() {
			return todo.overlapsTimeRange(start, end), nil
		}
		overlaps = func(occStart time.Time) bool {
			return todo.at(occStart).overlapsTimeRange(start, end)
		}
	default:
		// TODO handle more than just events and to-dos. A range with only an
		// end used to be ignored, so keep matching it here.
		if start.IsZero() {
			return true, nil
		}
		if rset == nil {
			return false, nil
		}
		overlaps = func(occStart time.Time) bool {
			return overlapsTimeRange(start, end, occStart, occStart)
		}
	}

	// Occurrences come in order, and none starting after the end of the range
	// can overlap it. Stop at the first one that does: a rule may have no end,
	// and with an open start the first occurrence decides.
	next := rset.Iterator()
	for occStart, ok := next(); ok; occStart, ok = next() {
		if !end.IsZero() && occStart.After(end) {
			break
		}
		if overlaps(occStart) {
			return true, nil
		}
	}
	return false, nil
}

// todoTimes holds the properties of a VTODO which decide whether it overlaps a
// time range. Absent properties are zero.
type todoTimes struct {
	dtstart, due, completed, created time.Time
	duration                         time.Duration
	hasDuration                      bool
}

func newTodoTimes(comp *ical.Component) (todoTimes, error) {
	var todo todoTimes
	for _, p := range []struct {
		name string
		dst  *time.Time
	}{
		{ical.PropDateTimeStart, &todo.dtstart},
		{ical.PropDue, &todo.due},
		{ical.PropCompleted, &todo.completed},
		{ical.PropCreated, &todo.created},
	} {
		t, err := comp.Props.DateTime(p.name, time.UTC)
		if err != nil {
			return todoTimes{}, err
		}
		*p.dst = t
	}
	if prop := comp.Props.Get(ical.PropDuration); prop != nil {
		duration, err := prop.Duration()
		if err != nil {
			return todoTimes{}, err
		}
		todo.duration = duration
		todo.hasDuration = true
	}
	return todo, nil
}

// at returns the times of the occurrence of a recurring VTODO starting at
// occStart.
func (todo todoTimes) at(occStart time.Time) todoTimes {
	occ := todo
	occ.dtstart = occStart
	if !todo.due.IsZero() {
		occ.due = occStart.Add(todo.due.Sub(todo.dtstart))
	}
	return occ
}

// overlapsTimeRange reports whether the VTODO overlaps the time range
// [start, end), as defined by the table in RFC 4791 section 9.9. A zero start
// or end leaves the range open on that side.
func (todo todoTimes) overlapsTimeRange(start, end time.Time) bool {
	startLT := func(t time.Time) bool { return start.IsZero() || start.Before(t) }
	startLE := func(t time.Time) bool { return start.IsZero() || !start.After(t) }
	endGT := func(t time.Time) bool { return end.IsZero() || end.After(t) }
	endGE := func(t time.Time) bool { return end.IsZero() || !end.Before(t) }

	switch {
	case !todo.dtstart.IsZero() && !todo.due.IsZero():
		return (startLT(todo.due) || startLE(todo.dtstart)) &&
			(endGT(todo.dtstart) || endGE(todo.due))
	case !todo.dtstart.IsZero() && todo.hasDuration:
		dtend := todo.dtstart.Add(todo.duration)
		return startLE(dtend) && (endGT(todo.dtstart) || endGE(dtend))
	case !todo.dtstart.IsZero():
		return startLE(todo.dtstart) && endGT(todo.dtstart)
	case !todo.due.IsZero():
		return startLT(todo.due) && endGE(todo.due)
	case !todo.completed.IsZero() && !todo.created.IsZero():
		return (startLE(todo.created) || startLE(todo.completed)) &&
			(endGE(todo.created) || endGE(todo.completed))
	case !todo.completed.IsZero():
		return startLE(todo.completed) && endGE(todo.completed)
	case !todo.created.IsZero():
		return endGT(todo.created)
	default:
		return true
	}
}

// overlapsTimeRange reports whether [compStart, compEnd) overlaps the time range
// [start, end). A component with no duration overlaps it when it starts inside
// it. A zero start or end leaves the range open on that side.
func overlapsTimeRange(start, end, compStart, compEnd time.Time) bool {
	if !end.IsZero() && !end.After(compStart) {
		return false
	}
	if compEnd.Equal(compStart) {
		return !compStart.Before(start)
	}
	return compEnd.After(start)
}

func matchPropTimeRange(start, end time.Time, field *ical.Prop) (bool, error) {
	// See https://datatracker.ietf.org/doc/html/rfc4791#section-9.9
	ptime, err := field.DateTime(time.UTC)
	if err != nil {
		return false, err
	}
	return !ptime.Before(start) && (end.IsZero() || ptime.Before(end)), nil
}

func matchParamFilter(filter ParamFilter, field *ical.Prop) (bool, error) {
	values := field.Params.Values(filter.Name)
	if len(values) == 0 {
		return filter.IsNotDefined, nil
	}
	if filter.IsNotDefined {
		return false, nil
	}
	if filter.TextMatch != nil {
		tm := *filter.TextMatch
		tm.NegateCondition = false
		matched := false
		for _, v := range values {
			m, err := matchTextMatch(tm, v)
			if err != nil {
				return false, err
			}
			if m {
				matched = true
				break
			}
		}
		if filter.TextMatch.NegateCondition {
			matched = !matched
		}
		return matched, nil
	}
	return true, nil
}

func matchTextMatch(txt TextMatch, value string) (bool, error) {
	valKey, ok := internal.CollateKey(value, txt.Collation, internal.CollationASCIICasemap)
	if !ok {
		return false, newPreconditionError(http.StatusForbidden, PreconditionSupportedCollation)
	}
	subKey, _ := internal.CollateKey(txt.Text, txt.Collation, internal.CollationASCIICasemap)
	match := strings.Contains(valKey, subKey)
	if txt.NegateCondition {
		match = !match
	}
	return match, nil
}
