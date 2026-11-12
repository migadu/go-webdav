// Package caldav provides a client and server CalDAV implementation.
//
// CalDAV is defined in RFC 4791.
package caldav

import (
	"fmt"
	"io"
	"time"

	"github.com/emersion/go-ical"
	"github.com/emersion/go-webdav"
	"github.com/emersion/go-webdav/internal"
)

var CapabilityCalendar = webdav.Capability("calendar-access")

func NewCalendarHomeSet(path string) webdav.BackendSuppliedHomeSet {
	return &calendarHomeSet{Hrefs: []internal.Href{{Path: path}}}
}

// ValidateCalendarObject checks the validity of a calendar object according to
// RFC 5545 and to the constraints laid out in RFC 4791 section 4.1, and
// returns the only event type and UID occurring in this calendar, or an error
// if the calendar could not be validated.
//
// A calendar object with only VTIMEZONE components is valid (RFC 5545 section
// 3.6): its event type is VTIMEZONE, and its UID is empty, since VTIMEZONE has
// no UID property. Such an object specifies no UID, so it can't conflict with
// another one (CALDAV:no-uid-conflict). Only calendars whose
// SupportedComponentSet lists VTIMEZONE hold such objects (RFC 4791 section
// 5.2.3): Handler checks it for PUT, and CalendarObjectCopyMover must check it
// for COPY and MOVE.
func ValidateCalendarObject(cal *ical.Calendar) (eventType string, uid string, err error) {
	// The encoder checks the components and properties required or allowed
	// by RFC 5545. A calendar it rejects couldn't be served back to clients
	// either.
	if err := ical.NewEncoder(io.Discard).Encode(cal); err != nil {
		return "", "", fmt.Errorf("invalid iCalendar object: %w", err)
	}

	// Calendar object resources contained in calendar collections
	// MUST NOT specify the iCalendar METHOD property.
	if prop := cal.Props.Get(ical.PropMethod); prop != nil {
		return "", "", fmt.Errorf("calendar resource must not specify METHOD property")
	}

	for _, comp := range cal.Children {
		if comp.Name == ical.CompTimezone {
			continue
		}

		// Calendar object resources contained in calendar collections
		// MUST NOT contain more than one type of calendar component
		// (e.g., VEVENT, VTODO, VJOURNAL, VFREEBUSY, etc.) with the
		// exception of VTIMEZONE components, which MUST be specified
		// for each unique TZID parameter value specified in the
		// iCalendar object.
		if eventType == "" {
			eventType = comp.Name
		}
		if eventType != comp.Name {
			return "", "", fmt.Errorf("conflicting event types in calendar: %s, %s", eventType, comp.Name)
		}
		// TODO check VTIMEZONE for each TZID?

		// RFC 5545 section 3.6.1 requires DTSTART on VEVENT when METHOD is
		// absent (and RFC 4791 section 4.1 forbids METHOD above).
		if comp.Name == ical.CompEvent && comp.Props.Get(ical.PropDateTimeStart) == nil {
			return "", "", fmt.Errorf("VEVENT component must specify %s property", ical.PropDateTimeStart)
		}

		// Calendar components in a calendar collection that have
		// different UID property values MUST be stored in separate
		// calendar object resources.
		compUID, err := comp.Props.Text(ical.PropUID)
		if err != nil {
			return "", "", fmt.Errorf("error checking component UID: %v", err)
		}
		if compUID == "" {
			return "", "", fmt.Errorf("calendar component %s must specify a non-empty %s property", comp.Name, ical.PropUID)
		}
		if uid == "" {
			uid = compUID
		}
		if uid != compUID {
			return "", "", fmt.Errorf("conflicting UID values in calendar: %s, %s", uid, compUID)
		}
	}
	if eventType == "" {
		// The encoder checked there is a component: they're all VTIMEZONE
		return ical.CompTimezone, "", nil
	}
	return eventType, uid, nil
}

type Calendar struct {
	Path            string
	Name            string
	Description     string
	Color           string
	MaxResourceSize int64
	SyncToken       string // the current DAV:sync-token, see SyncBackend
	// SupportedComponentSet lists the component types of the calendar
	// objects the calendar holds, reported as the
	// CALDAV:supported-calendar-component-set property (RFC 4791 section
	// 5.2.3). If nil, the server reports VEVENT only. Calendar objects with
	// only VTIMEZONE components can only be stored in calendars listing
	// VTIMEZONE.
	SupportedComponentSet []string
	Timezone              *ical.Calendar
	// ReadOnly reports that the current user may only read this calendar. It
	// controls the DAV:current-user-privilege-set reported by the server.
	// Clients set it from that property, and leave it false if the server
	// doesn't return it.
	ReadOnly bool

	// CTag, when set, is reported as the CalendarServer getctag property: an
	// opaque token that must change whenever the calendar's contents change.
	CTag string
}

// CalendarUpdate holds the changes a PROPPATCH request makes to a calendar.
// A nil field is left as is; removing a string property sets it to the empty
// string, and removing the calendar timezone sets RemoveTimezone to true.
type CalendarUpdate struct {
	Name           *string
	Description    *string
	Color          *string
	Timezone       *ical.Calendar
	RemoveTimezone bool
}

type CalendarCompRequest struct {
	Name string

	AllProps bool
	Props    []string

	AllComps bool
	Comps    []CalendarCompRequest

	// The fields below apply to the whole calendar data, so only the
	// outermost request has them. The server passes them on: applying them
	// to the calendar objects returned is up to the backend.

	// Expand asks for recurring components to be expanded into the instances
	// overlapping the time range (RFC 4791 section 9.6.5).
	Expand *CalendarExpandRequest
	// LimitRecurrenceSet asks for, of the overridden instances of each
	// recurring component, only those which impact the time range: their
	// current or original time overlaps it, or, through the RANGE parameter
	// of their RECURRENCE-ID, they change instances which do. The master
	// component is still returned (RFC 4791 section 9.6.6). It can't be used
	// with Expand.
	LimitRecurrenceSet *CalendarLimitRequest
	// LimitFreeBusySet asks for only the FREEBUSY property values of
	// VFREEBUSY components which overlap the time range (RFC 4791 section
	// 9.6.7).
	LimitFreeBusySet *CalendarLimitRequest
}

// CalendarExpandRequest is the time range of an expand request. Start is
// inclusive, End is exclusive and after Start. Both are required: a client
// returns an error for a zero time. A client sends whole seconds, so End must
// still be after Start once both are truncated to the second.
type CalendarExpandRequest struct {
	Start, End time.Time
}

// CalendarLimitRequest is the time range of a limit-recurrence-set or
// limit-freebusy-set request. Start is inclusive, End is exclusive and after
// Start. Both are required: a client returns an error for a zero time. A
// client sends whole seconds, so End must still be after Start once both are
// truncated to the second.
type CalendarLimitRequest struct {
	Start, End time.Time
}

type CompFilter struct {
	Name         string
	IsNotDefined bool
	Start, End   time.Time
	Props        []PropFilter
	Comps        []CompFilter
}

type ParamFilter struct {
	Name         string
	IsNotDefined bool
	TextMatch    *TextMatch
}

type PropFilter struct {
	Name         string
	IsNotDefined bool
	Start, End   time.Time
	TextMatch    *TextMatch
	ParamFilter  []ParamFilter
}

type TextMatch struct {
	Text            string
	Collation       string // defaults to "i;ascii-casemap"
	NegateCondition bool
}

type CalendarQuery struct {
	CompRequest CalendarCompRequest
	CompFilter  CompFilter
}

type CalendarMultiGet struct {
	Paths       []string
	CompRequest CalendarCompRequest
}

type CalendarObject struct {
	Path          string
	ModTime       time.Time
	ContentLength int64
	ETag          string
	Data          *ical.Calendar
	// ReadOnly reports that the current user may only read this object. It
	// controls the DAV:current-user-privilege-set reported by the server.
	ReadOnly bool
}

// SyncQuery is the query struct represents a sync-collection request
type SyncQuery struct {
	CompRequest CalendarCompRequest
	SyncToken   string
	Limit       int // <= 0 means unlimited
}

// SyncResponse contains the returned sync-token for next time
type SyncResponse struct {
	SyncToken string
	Updated   []CalendarObject
	Deleted   []string
	// Truncated reports that the response only holds part of the changes,
	// for instance because of SyncQuery.Limit (RFC 6578 section 3.6).
	// SyncToken then identifies the partial state: a new request with it
	// returns the remaining changes. Server backends set it when they stop
	// early; the client sets it when the server reports a truncated result.
	Truncated bool
}

// Inbox is the scheduling Inbox collection of a user, where the scheduling
// messages delivered to the user are stored (RFC 6638 section 2.2). See
// InboxBackend.
type Inbox struct {
	// Path is the path of the Inbox collection, which mustn't be inside a
	// calendar. It's served as the CALDAV:schedule-inbox-URL property of the
	// principal, and the Inbox is listed along with the calendars when it's
	// a member of the calendar home set, such as "/user/calendars/inbox/".
	// The paths of the scheduling messages are those of its members.
	Path string
	// UserAddressSet lists the calendar user addresses of the user, such as
	// "mailto:user@example.com", which identify the user as the ORGANIZER or
	// an ATTENDEE of events. It's served as the
	// CALDAV:calendar-user-address-set property of the principal (RFC 6638
	// section 2.4.1).
	UserAddressSet []string
	// ReadOnly reports that the current user may only read the scheduling
	// messages in the Inbox, not update or remove them. It controls the
	// DAV:current-user-privilege-set reported by the server for the Inbox;
	// as for calendars, that of the messages is controlled by their
	// CalendarObject.ReadOnly.
	ReadOnly bool
}
