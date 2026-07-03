package monopoly

// board.go defines the canonical 40-space Monopoly board as immutable data. The
// engine reads this table; it never mutates it. Keeping the board as pure data
// (prices, rent tiers, color groups) is what lets the rules code stay small and
// makes every rent/decision auditable against a single source of truth.

// SpaceKind classifies a board square.
type SpaceKind string

const (
	KindGo             SpaceKind = "go"
	KindStreet         SpaceKind = "street"
	KindRailroad       SpaceKind = "railroad"
	KindUtility        SpaceKind = "utility"
	KindTax            SpaceKind = "tax"
	KindChance         SpaceKind = "chance"
	KindCommunityChest SpaceKind = "community_chest"
	KindJail           SpaceKind = "jail" // "Just Visiting" / the jail cell
	KindFreeParking    SpaceKind = "free_parking"
	KindGoToJail       SpaceKind = "go_to_jail"
)

// Color groups and the special pseudo-groups for railroads/utilities.
const (
	GroupBrown     = "brown"
	GroupLightBlue = "light_blue"
	GroupPink      = "pink"
	GroupOrange    = "orange"
	GroupRed       = "red"
	GroupYellow    = "yellow"
	GroupGreen     = "green"
	GroupDarkBlue  = "dark_blue"
	GroupRailroad  = "railroad"
	GroupUtility   = "utility"
)

// Key fixed board indices the rules reference by name.
const (
	IdxGo         = 0
	IdxJail       = 10 // also "Just Visiting"
	IdxFreeParkng = 20
	IdxGoToJail   = 30
	BoardSize     = 40
)

// Space is one immutable board square. Rent holds [base, 1h, 2h, 3h, 4h, hotel]
// for streets; HouseCost is the per-house build cost for the group. For railroads
// and utilities rent is computed from ownership count / dice, so Rent is unused.
type Space struct {
	Index     int       `json:"index"`
	Name      string    `json:"name"`
	Kind      SpaceKind `json:"kind"`
	Group     string    `json:"group,omitempty"`
	Price     int       `json:"price,omitempty"`      // street/railroad/utility list price
	Rent      [6]int    `json:"rent,omitempty"`       // streets only
	HouseCost int       `json:"house_cost,omitempty"` // streets only
	Tax       int       `json:"tax,omitempty"`        // tax squares only
}

// Ownable reports whether the square can be bought (street/railroad/utility).
func (s Space) Ownable() bool {
	return s.Kind == KindStreet || s.Kind == KindRailroad || s.Kind == KindUtility
}

// MortgageValue is half the list price (standard rule).
func (s Space) MortgageValue() int { return s.Price / 2 }

// boardData is the canonical US-edition board. It is returned via Board() as a
// fresh copy so callers cannot mutate the shared table.
var boardData = [BoardSize]Space{
	{Index: 0, Name: "GO", Kind: KindGo},
	{Index: 1, Name: "Mediterranean Avenue", Kind: KindStreet, Group: GroupBrown, Price: 60, Rent: [6]int{2, 10, 30, 90, 160, 250}, HouseCost: 50},
	{Index: 2, Name: "Community Chest", Kind: KindCommunityChest},
	{Index: 3, Name: "Baltic Avenue", Kind: KindStreet, Group: GroupBrown, Price: 60, Rent: [6]int{4, 20, 60, 180, 320, 450}, HouseCost: 50},
	{Index: 4, Name: "Income Tax", Kind: KindTax, Tax: 200},
	{Index: 5, Name: "Reading Railroad", Kind: KindRailroad, Group: GroupRailroad, Price: 200},
	{Index: 6, Name: "Oriental Avenue", Kind: KindStreet, Group: GroupLightBlue, Price: 100, Rent: [6]int{6, 30, 90, 270, 400, 550}, HouseCost: 50},
	{Index: 7, Name: "Chance", Kind: KindChance},
	{Index: 8, Name: "Vermont Avenue", Kind: KindStreet, Group: GroupLightBlue, Price: 100, Rent: [6]int{6, 30, 90, 270, 400, 550}, HouseCost: 50},
	{Index: 9, Name: "Connecticut Avenue", Kind: KindStreet, Group: GroupLightBlue, Price: 120, Rent: [6]int{8, 40, 100, 300, 450, 600}, HouseCost: 50},
	{Index: 10, Name: "Jail / Just Visiting", Kind: KindJail},
	{Index: 11, Name: "St. Charles Place", Kind: KindStreet, Group: GroupPink, Price: 140, Rent: [6]int{10, 50, 150, 450, 625, 750}, HouseCost: 100},
	{Index: 12, Name: "Electric Company", Kind: KindUtility, Group: GroupUtility, Price: 150},
	{Index: 13, Name: "States Avenue", Kind: KindStreet, Group: GroupPink, Price: 140, Rent: [6]int{10, 50, 150, 450, 625, 750}, HouseCost: 100},
	{Index: 14, Name: "Virginia Avenue", Kind: KindStreet, Group: GroupPink, Price: 160, Rent: [6]int{12, 60, 180, 500, 700, 900}, HouseCost: 100},
	{Index: 15, Name: "Pennsylvania Railroad", Kind: KindRailroad, Group: GroupRailroad, Price: 200},
	{Index: 16, Name: "St. James Place", Kind: KindStreet, Group: GroupOrange, Price: 180, Rent: [6]int{14, 70, 200, 550, 750, 950}, HouseCost: 100},
	{Index: 17, Name: "Community Chest", Kind: KindCommunityChest},
	{Index: 18, Name: "Tennessee Avenue", Kind: KindStreet, Group: GroupOrange, Price: 180, Rent: [6]int{14, 70, 200, 550, 750, 950}, HouseCost: 100},
	{Index: 19, Name: "New York Avenue", Kind: KindStreet, Group: GroupOrange, Price: 200, Rent: [6]int{16, 80, 220, 600, 800, 1000}, HouseCost: 100},
	{Index: 20, Name: "Free Parking", Kind: KindFreeParking},
	{Index: 21, Name: "Kentucky Avenue", Kind: KindStreet, Group: GroupRed, Price: 220, Rent: [6]int{18, 90, 250, 700, 875, 1050}, HouseCost: 150},
	{Index: 22, Name: "Chance", Kind: KindChance},
	{Index: 23, Name: "Indiana Avenue", Kind: KindStreet, Group: GroupRed, Price: 220, Rent: [6]int{18, 90, 250, 700, 875, 1050}, HouseCost: 150},
	{Index: 24, Name: "Illinois Avenue", Kind: KindStreet, Group: GroupRed, Price: 240, Rent: [6]int{20, 100, 300, 750, 925, 1100}, HouseCost: 150},
	{Index: 25, Name: "B&O Railroad", Kind: KindRailroad, Group: GroupRailroad, Price: 200},
	{Index: 26, Name: "Atlantic Avenue", Kind: KindStreet, Group: GroupYellow, Price: 260, Rent: [6]int{22, 110, 330, 800, 975, 1150}, HouseCost: 150},
	{Index: 27, Name: "Ventnor Avenue", Kind: KindStreet, Group: GroupYellow, Price: 260, Rent: [6]int{22, 110, 330, 800, 975, 1150}, HouseCost: 150},
	{Index: 28, Name: "Water Works", Kind: KindUtility, Group: GroupUtility, Price: 150},
	{Index: 29, Name: "Marvin Gardens", Kind: KindStreet, Group: GroupYellow, Price: 280, Rent: [6]int{24, 120, 360, 850, 1025, 1200}, HouseCost: 150},
	{Index: 30, Name: "Go To Jail", Kind: KindGoToJail},
	{Index: 31, Name: "Pacific Avenue", Kind: KindStreet, Group: GroupGreen, Price: 300, Rent: [6]int{26, 130, 390, 900, 1100, 1275}, HouseCost: 200},
	{Index: 32, Name: "North Carolina Avenue", Kind: KindStreet, Group: GroupGreen, Price: 300, Rent: [6]int{26, 130, 390, 900, 1100, 1275}, HouseCost: 200},
	{Index: 33, Name: "Community Chest", Kind: KindCommunityChest},
	{Index: 34, Name: "Pennsylvania Avenue", Kind: KindStreet, Group: GroupGreen, Price: 320, Rent: [6]int{28, 150, 450, 1000, 1200, 1400}, HouseCost: 200},
	{Index: 35, Name: "Short Line Railroad", Kind: KindRailroad, Group: GroupRailroad, Price: 200},
	{Index: 36, Name: "Chance", Kind: KindChance},
	{Index: 37, Name: "Park Place", Kind: KindStreet, Group: GroupDarkBlue, Price: 350, Rent: [6]int{35, 175, 500, 1100, 1300, 1500}, HouseCost: 200},
	{Index: 38, Name: "Luxury Tax", Kind: KindTax, Tax: 100},
	{Index: 39, Name: "Boardwalk", Kind: KindStreet, Group: GroupDarkBlue, Price: 400, Rent: [6]int{50, 200, 600, 1400, 1700, 2000}, HouseCost: 200},
}

// Board returns a fresh copy of the canonical board.
func Board() []Space {
	out := make([]Space, BoardSize)
	copy(out, boardData[:])
	return out
}

// space returns the immutable square at index i.
func space(i int) Space { return boardData[i] }

// groupMembers lists the board indices in each color/pseudo group.
var groupMembers = func() map[string][]int {
	m := map[string][]int{}
	for _, sp := range boardData {
		if sp.Group != "" {
			m[sp.Group] = append(m[sp.Group], sp.Index)
		}
	}
	return m
}()

// railroadRentTable maps "railroads owned" -> rent.
var railroadRentTable = [5]int{0, 25, 50, 100, 200}
