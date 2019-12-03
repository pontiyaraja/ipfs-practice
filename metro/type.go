package metro

//http://svc.metrotransit.org/NexTrip/{ROUTE}/{DIRECTION}/{STOP}
// {
// 	"Actual": false,
// 	"BlockNumber": 9,
// 	"DepartureText": "4:35",
// 	"DepartureTime": "\/Date(1563788100000-0500)\/",
// 	"Description": "to Mpls-Target Field",
// 	"Gate": "1",
// 	"Route": "Blue",
// 	"RouteDirection": "NORTHBOUND",
// 	"Terminal": "",
// 	"VehicleHeading": 0,
// 	"VehicleLatitude": 0,
// 	"VehicleLongitude": 0
// }

//http://svc.metrotransit.org/NexTrip/Stops/{ROUTE}/{DIRECTION}
// {
// 	"Text": "Mall of America Station",
// 	"Value": "MAAM"
// }

//http://svc.metrotransit.org/NexTrip/Routes
// {
// 	"Description": "METRO Blue Line",
// 	"ProviderID": "8",
// 	"Route": "901"
// }

//http://svc.metrotransit.org/NexTrip/Directions/{ROUTE}
// {
// 	"Text": "NORTHBOUND",
// 	"Value": "4"
// }

type Direction struct {
	Text  string `json:"Text"`
	Value string `json:"Value"`
}
type Routes struct {
	Description string `json:"Description"`
	ProviderID  string `json:"ProviderID"`
	Route       string `json:"Route"`
}
type Stops struct {
	Text  string `json:"Text"`
	Value string `json:"Value"`
}
type TimeDepartures struct {
	Actual           bool    `json:"Actual"`
	BlockNumber      int     `json:"BlockNumber"`
	DepartureText    string  `json:"DepartureText"`
	DepartureTime    string  `json:"DepartureTime"`
	Description      string  `json:"Description"`
	Gate             string  `json:"Gate"`
	Route            string  `json:"Route"`
	RouteDirection   string  `json:"RouteDirection"`
	Terminal         string  `json:"Terminal"`
	VehicleHeading   int     `json:"VehicleHeading"`
	VehicleLatitude  float64 `json:"VehicleLatitude"`
	VehicleLongitude float64 `json:"VehicleLongitude"`
}
