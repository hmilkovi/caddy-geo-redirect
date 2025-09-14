package geoip

import (
	"math"
	"testing"
)

func TestHaversineDistance(t *testing.T) {
	tests := []struct {
		name                   string
		lat1, lon1, lat2, lon2 float64
		want                   float64
	}{
		{
			name: "Same point",
			lat1: 40.7128, lon1: -74.0060,
			lat2: 40.7128, lon2: -74.0060,
			want: 0.0,
		},
		{
			name: "New York to London",
			lat1: 40.7128, lon1: -74.0060,
			lat2: 51.5074, lon2: -0.1278,
			want: 5570.222, // Approximate distance in km
		},
		{
			name: "Equator points",
			lat1: 0.0, lon1: 0.0,
			lat2: 0.0, lon2: 180.0,
			want: 20015.087, // Half the circumference of the Earth
		},
		{
			name: "North Pole to South Pole",
			lat1: 90.0, lon1: 0.0,
			lat2: -90.0, lon2: 0.0,
			want: 20015.087, // Half the circumference of the Earth
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := HaversineDistance(tt.lat1, tt.lon1, tt.lat2, tt.lon2)
			if math.Abs(got-tt.want) > 0.001 {
				t.Errorf("HaversineDistance() = %v, want %v", got, tt.want)
			}
		})
	}
}
