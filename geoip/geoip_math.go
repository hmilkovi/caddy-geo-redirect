package geoip

import "math"

// HaversineDistance calculates the distance between two points on Earth.
func HaversineDistance(lat1, lon1, lat2, lon2 float64) float64 {
	// Convert degrees to radians
	lat1Rad := lat1 * math.Pi / 180
	lon1Rad := lon1 * math.Pi / 180
	lat2Rad := lat2 * math.Pi / 180
	lon2Rad := lon2 * math.Pi / 180

	diffLat := lat2Rad - lat1Rad
	diffLon := lon2Rad - lon1Rad

	a := math.Pow(math.Sin(diffLat/2), 2) + math.Cos(lat1Rad)*math.Cos(lat2Rad)*math.Pow(math.Sin(diffLon/2), 2)
	c := 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))

	// earthRadiusKm is the mean radius of Earth in kilometers is 6371.0
	return 6371.0 * c
}
