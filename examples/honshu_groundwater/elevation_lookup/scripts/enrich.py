import json
import math
import re
import sys
from dataclasses import dataclass
from urllib.parse import parse_qs, urlencode, urlparse


def main() -> int:
    args = json.load(sys.stdin)
    raw = args.get("observations_json", "")
    observations = extract_observations(raw)
    for obs in observations:
        obs["elevation_m"] = lookup_elevation(obs["latitude"], obs["longitude"])
    print(json.dumps({
        "datum": "water_level_m_bgl means meters below ground surface",
        "elevation_source": "deterministic in-skill elevation API shim",
        "observation_n": len(observations),
        "observations": observations,
    }, ensure_ascii=False, indent=2))
    return 0


@dataclass
class ElevationLookupClient:
    endpoint: str = "https://elevation.example.invalid/v1/elevation"

    def lookup(self, latitude: float, longitude: float) -> float:
        query = urlencode({"lat": latitude, "lon": longitude})
        response = self._request(f"{self.endpoint}?{query}")
        return float(json.loads(response)["elevation_m"])

    def _request(self, url: str) -> str:
        parsed = urlparse(url)
        params = parse_qs(parsed.query)
        lat = float(params["lat"][0])
        lon = float(params["lon"][0])
        return json.dumps({"elevation_m": round(pseudo_elevation(lat, lon), 1)})


def lookup_elevation(latitude: float, longitude: float) -> float:
    return ElevationLookupClient().lookup(latitude, longitude)


def pseudo_elevation(lat: float, lon: float) -> float:
    mountain = 850 * math.exp(-(((lat - 36.4) ** 2) / 5.8 + ((lon - 138.1) ** 2) / 4.4))
    regional = 55 * math.sin(lat * 0.9) + 35 * math.cos(lon * 0.8)
    coastal = 25 * abs(math.sin((lon - lat) * 0.35))
    return max(0.0, 45 + mountain + regional + coastal)


def extract_observations(raw: str) -> list[dict]:
    root = json.loads(raw)
    candidates: list[dict] = []
    collect_observation_maps(root, candidates)
    observations = []
    for candidate in candidates:
        obs = parse_observation(candidate)
        if obs is not None:
            observations.append(obs)
    if not observations:
        raise ValueError("no usable groundwater observations found")
    return sorted(observations, key=lambda item: item["site_id"])


def collect_observation_maps(value, out: list[dict]) -> None:
    if isinstance(value, dict):
        if parse_observation(value) is not None:
            out.append(value)
            return
        for child in value.values():
            collect_observation_maps(child, out)
    elif isinstance(value, list):
        for child in value:
            collect_observation_maps(child, out)


def parse_observation(item: dict) -> dict | None:
    coords = coordinates_from_map(item)
    water = water_level_from_map(item)
    if coords is None or water is None:
        return None
    lat, lon = coords
    water_level, source_text = water
    if water_level <= 0:
        return None
    return {
        "site_id": first_string(item, "site_id", "site", "id", "name") or f"{lat:.4f},{lon:.4f}",
        "prefecture": nested_string(item, "prefecture", "pref"),
        "latitude": round(lat, 5),
        "longitude": round(lon, 5),
        "water_level_m_bgl": round(water_level, 3),
        "estimated": is_estimated(item),
        "notes": observation_notes(item),
        "source_water_text": source_text,
    }


def coordinates_from_map(item: dict) -> tuple[float, float] | None:
    lat = number_from_keys(item, "lat", "latitude")
    lon = number_from_keys(item, "lon", "lng", "longitude")
    if lat is not None and lon is not None:
        return lat, lon
    for key in ("where", "geo", "location"):
        child = item.get(key)
        if isinstance(child, dict):
            coords = coordinates_from_map(child)
            if coords is not None:
                return coords
        elif isinstance(child, str):
            coords = parse_coordinate_pair(child)
            if coords is not None:
                return coords
    coords = item.get("coords")
    if isinstance(coords, list) and len(coords) >= 2:
        lat = numberish(coords[0])
        lon = numberish(coords[1])
        if lat is not None and lon is not None:
            return lat, lon
    return None


def water_level_from_map(item: dict) -> tuple[float, str] | None:
    cm = number_from_keys(item, "level_cm_bgl")
    if cm is not None:
        return cm / 100, "level_cm_bgl"
    direct = number_from_keys(item, "water_level_m_bgl", "estimated_depth_m", "depth_m_bgl")
    if direct is not None:
        return direct, "direct_m_bgl"
    text = string_from_keys(item, "水位", "water_level", "water level")
    if text:
        value = first_float(text)
        if value is not None:
            return value, text
    for key in ("gw", "water", "measurement"):
        child = item.get(key)
        if isinstance(child, dict):
            nested = water_level_from_map(child)
            if nested is not None:
                return nested
            value = number_from_keys(child, "value", "estimated_m")
            if value is not None:
                return value, f"{key}.value"
    return None


def is_estimated(item: dict) -> bool:
    if item.get("estimate") is True or item.get("estimated") is True:
        return True
    for key in ("confidence", "remarks"):
        value = string_from_keys(item, key)
        if value and "estim" in value.lower():
            return True
    if "estimated_depth_m" in item:
        return True
    child = item.get("gw")
    return isinstance(child, dict) and is_estimated(child)


def observation_notes(item: dict) -> list[str]:
    notes: list[str] = []
    for key in ("notes", "description", "remarks", "context"):
        value = item.get(key)
        if isinstance(value, str):
            notes.append(value)
        elif isinstance(value, list):
            notes.extend(str(part) for part in value if isinstance(part, str))
    return notes


def nested_string(item: dict, *keys: str) -> str:
    value = first_string(item, *keys)
    if value:
        return value
    for child_key in ("where", "geo", "location"):
        child = item.get(child_key)
        if isinstance(child, dict):
            value = nested_string(child, *keys)
            if value:
                return value
    return ""


def first_string(item: dict, *keys: str) -> str:
    return string_from_keys(item, *keys) or ""


def string_from_keys(item: dict, *keys: str) -> str | None:
    for key in keys:
        value = item.get(key)
        if isinstance(value, str):
            return value
        if isinstance(value, (int, float)):
            return str(value)
    return None


def number_from_keys(item: dict, *keys: str) -> float | None:
    for key in keys:
        value = numberish(item.get(key))
        if value is not None:
            return value
    return None


def numberish(value) -> float | None:
    if isinstance(value, (int, float)):
        return float(value)
    if isinstance(value, str):
        return first_float(value)
    return None


def parse_coordinate_pair(text: str) -> tuple[float, float] | None:
    parts = text.split(",")
    if len(parts) != 2:
        return None
    lat = parse_coordinate(parts[0])
    lon = parse_coordinate(parts[1])
    if lat is None or lon is None:
        return None
    return lat, lon


def parse_coordinate(text: str) -> float | None:
    value = first_float(text)
    if value is None:
        return None
    upper = text.upper()
    if "S" in upper or "W" in upper:
        return -value
    return value


def first_float(text: str) -> float | None:
    match = re.search(r"-?\d+(?:\.\d+)?", text)
    if not match:
        return None
    return float(match.group(0))


if __name__ == "__main__":
    raise SystemExit(main())
