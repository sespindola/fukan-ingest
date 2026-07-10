package cables

import "testing"

func TestComputeBBox(t *testing.T) {
	coords := []int32{
		ScaleCoord(40.0), ScaleCoord(-74.0),
		ScaleCoord(51.5), ScaleCoord(-0.1),
		ScaleCoord(45.0), ScaleCoord(-30.0),
	}
	minLat, minLon, maxLat, maxLon := ComputeBBox(coords)
	if DescaleCoord(minLat) != 40.0 || DescaleCoord(maxLat) != 51.5 {
		t.Errorf("lat bbox = [%v,%v], want [40, 51.5]", DescaleCoord(minLat), DescaleCoord(maxLat))
	}
	if DescaleCoord(minLon) != -74.0 || DescaleCoord(maxLon) != -0.1 {
		t.Errorf("lon bbox = [%v,%v], want [-74, -0.1]", DescaleCoord(minLon), DescaleCoord(maxLon))
	}
}
