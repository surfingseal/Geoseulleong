// Package kakao implements only the documented Kakao Developers routing REST APIs.
package kakao

import (
	"geoseulleong/internal/odsay"
	"time"
)

const TransitEndpoint = "https://dapi.kakao.com/v2/routing/publictraffic"
const WalkingEndpoint = "https://dapi.kakao.com/v2/routing/walk"

type Request struct {
	Start odsay.Point `json:"start"`
	End   odsay.Point `json:"end"`
}
type WalkingRequest struct{ Start, End odsay.Point }
type Geometry struct {
	Points [][]float64 `json:"points"`
}
type Stop struct {
	Name string `json:"name"`
}
type Vehicle struct {
	Name string `json:"name"`
	Type string `json:"type"`
}
type Fare struct {
	Value *int `json:"value"`
	Min   *int `json:"min"`
	Max   *int `json:"max"`
}
type TransitProperties struct {
	Total      *int   `json:"total"`
	Bus        *int   `json:"bus"`
	Subway     *int   `json:"subway"`
	Mixed      *int   `json:"busAndSubway"`
	LandingURL string `json:"landingURL"`
}
type TransitRouteProperties struct {
	Type      string `json:"type"`
	Distance  *int   `json:"totalDistance"`
	Time      *int   `json:"totalTime"`
	Transfers *int   `json:"transfers"`
	Fare      Fare   `json:"fare"`
}
type TransitStepProperties struct {
	Guidance string    `json:"guidance"`
	Type     string    `json:"type"`
	Distance *int      `json:"distance"`
	Time     *int      `json:"time"`
	Stops    []Stop    `json:"stops"`
	Vehicles []Vehicle `json:"vehicles"`
}
type TransitStep struct {
	Properties TransitStepProperties `json:"properties"`
	Path       Geometry              `json:"path"`
}
type TransitRoute struct {
	Properties TransitRouteProperties `json:"properties"`
	Steps      []TransitStep          `json:"steps"`
}
type TransitResponse struct {
	Status     string            `json:"status"`
	Properties TransitProperties `json:"properties"`
	Routes     []TransitRoute    `json:"routes"`
}
type WalkProperties struct {
	Distance   *int   `json:"totalDistance"`
	Time       *int   `json:"totalTime"`
	LandingURL string `json:"landingUrl"`
}
type WalkLegProperties struct {
	Distance *int `json:"distance"`
	Time     *int `json:"time"`
}
type WalkStepProperties struct {
	Distance *int     `json:"distance"`
	Time     *int     `json:"time"`
	Guidance string   `json:"guidance"`
	X        *float64 `json:"x"`
	Y        *float64 `json:"y"`
}
type WalkStep struct {
	Properties WalkStepProperties `json:"properties"`
	Path       Geometry           `json:"path"`
}
type WalkLeg struct {
	Properties WalkLegProperties `json:"properties"`
	Steps      []WalkStep        `json:"steps"`
}
type WalkRoute struct {
	Properties WalkProperties `json:"properties"`
	Legs       []WalkLeg      `json:"legs"`
}
type WalkingResponse struct {
	Status string     `json:"status"`
	Route  *WalkRoute `json:"route"`
}

// Capture is private-tool provenance. Body is excluded from metadata JSON.
type Capture struct {
	Request    Request   `json:"request"`
	StartedAt  time.Time `json:"request_started_at"`
	ReceivedAt time.Time `json:"received_at"`
	HTTPStatus int       `json:"http_status"`
	Body       []byte    `json:"-"`
}
