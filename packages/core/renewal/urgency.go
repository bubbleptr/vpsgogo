package renewal

// Urgency is how soon a plan needs the user's attention.
type Urgency int

const (
	Calm    Urgency = iota // more than SoonDays left
	Soon                   // SoonDays or fewer left, the day it expires included
	Overdue                // expired
)

// SoonDays is the days left at which a plan becomes Soon: the first of
// DefaultRemindDays.
const SoonDays = 7

func (u Urgency) String() string {
	switch u {
	case Soon:
		return "soon"
	case Overdue:
		return "overdue"
	}
	return "calm"
}

// Urgency classifies s on today.
func (s Subscription) Urgency(today Date) Urgency {
	switch left := s.DaysLeft(today); {
	case left < 0:
		return Overdue
	case left <= SoonDays:
		return Soon
	}
	return Calm
}
