package model

import "errors"

func Validate(e FukanEvent) error {
	if e.AssetID == "" {
		return errors.New("empty asset_id")
	}
	if e.AssetType == "" {
		return errors.New("empty asset_type")
	}
	if e.Source == "" {
		return errors.New("empty source")
	}
	if e.Lat == 0 && e.Lon == 0 {
		return errors.New("null island coordinates (0,0)")
	}
	if e.Lat < -900_000_000 || e.Lat > 900_000_000 {
		return errors.New("latitude out of range")
	}
	if e.Lon < -1_800_000_000 || e.Lon > 1_800_000_000 {
		return errors.New("longitude out of range")
	}
	if e.Timestamp <= 0 {
		return errors.New("invalid timestamp")
	}
	return nil
}
