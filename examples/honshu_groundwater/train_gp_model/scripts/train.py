import csv
import json
import math
import os
import re
import sys
import tempfile
import warnings

os.environ.setdefault("MPLCONFIGDIR", os.path.join(tempfile.gettempdir(), "dango-matplotlib-cache"))
os.makedirs(os.environ["MPLCONFIGDIR"], exist_ok=True)

import matplotlib
import numpy as np
from sklearn.exceptions import ConvergenceWarning
from sklearn.gaussian_process import GaussianProcessRegressor
from sklearn.gaussian_process.kernels import ConstantKernel, RBF, WhiteKernel
from sklearn.metrics import mean_absolute_error
from sklearn.preprocessing import StandardScaler

matplotlib.use("Agg")
matplotlib.set_loglevel("error")
import matplotlib.pyplot as plt

warnings.filterwarnings("ignore", category=ConvergenceWarning)


def main() -> int:
    args = json.load(sys.stdin)
    parent_handoff = args.get("parent_handoff", "")
    artifacts_dir = args.get("artifacts_dir") or default_artifacts_dir()
    if not artifacts_dir:
        raise ValueError("artifacts_dir is required or DANGO_ARTIFACTS_DIR must be set")
    os.makedirs(artifacts_dir, exist_ok=True)

    enriched = json.loads(extract_last_json_block(parent_handoff))
    observations = enriched.get("observations", [])
    if not observations:
        raise ValueError("no enriched observations for model training")

    model, scaler, training_mae, fitted_kernel = fit_groundwater_gp(observations)
    predictions = predict_honshu_grid(model, scaler, observations)
    csv_path = os.path.join(artifacts_dir, "honshu_groundwater_predictions.csv")
    write_prediction_csv(csv_path, predictions)
    plot_path = os.path.join(artifacts_dir, "honshu_groundwater_surface.svg")
    write_prediction_plot(plot_path, predictions, observations)

    print(json.dumps({
        "model": "scikit-learn GaussianProcessRegressor with RBF and WhiteKernel",
        "kernel": str(fitted_kernel),
        "observation_count": len(observations),
        "prediction_count": len(predictions),
        "csv_path": csv_path,
        "plot_path": plot_path,
        "resources": [
            {
                "path": csv_path,
                "type": "file",
                "description": "Honshu groundwater prediction CSV for downstream analysis.",
            },
            {
                "path": plot_path,
                "type": "file",
                "description": "SVG visualization of the prediction surface.",
            },
        ],
        "mean_predicted_water_level_m_bgl": round(mean(row["predicted_water_level_m_bgl"] for row in predictions), 3),
        "validation_summary": f"In-sample MAE: {training_mae:.3f} m; uncertainty is the Gaussian process posterior standard deviation.",
        "downstream_reminder": "Use csv_path for analysis. PDF output was not requested and was intentionally not generated.",
    }, indent=2))
    return 0


def fit_groundwater_gp(observations: list[dict]) -> tuple[GaussianProcessRegressor, StandardScaler, float, object]:
    x = np.array([[obs["latitude"], obs["longitude"], obs["elevation_m"]] for obs in observations], dtype=float)
    y = np.array([obs["water_level_m_bgl"] for obs in observations], dtype=float)
    scaler = StandardScaler()
    x_scaled = scaler.fit_transform(x)
    kernel = (
        ConstantKernel(1.0, (0.1, 10.0))
        * RBF(length_scale=np.ones(x.shape[1]), length_scale_bounds=(0.05, 10.0))
        + WhiteKernel(noise_level=0.05, noise_level_bounds=(1e-4, 1.0))
    )
    model = GaussianProcessRegressor(
        kernel=kernel,
        alpha=1e-6,
        normalize_y=True,
        n_restarts_optimizer=0,
        random_state=42,
    )
    model.fit(x_scaled, y)
    fitted = model.predict(x_scaled)
    return model, scaler, float(mean_absolute_error(y, fitted)), model.kernel_


def predict_honshu_grid(model: GaussianProcessRegressor, scaler: StandardScaler, observations: list[dict]) -> list[dict]:
    rows = []
    features = []
    locations = []
    for lat in np.linspace(34.5, 40.5, 6):
        for lon in np.linspace(132.6, 140.1, 6):
            elevation = pseudo_elevation(float(lat), float(lon))
            features.append([lat, lon, elevation])
            locations.append((float(lat), float(lon), elevation))
    predicted, std = model.predict(scaler.transform(np.array(features, dtype=float)), return_std=True)
    for (lat, lon, elevation), value, uncertainty in zip(locations, predicted, std):
        nearest_id, nearest_distance = nearest_observation(lat, lon, observations)
        rows.append({
            "latitude": round(lat, 4),
            "longitude": round(lon, 4),
            "elevation_m": round(elevation, 1),
            "predicted_water_level_m_bgl": round(max(0.1, float(value)), 3),
            "uncertainty_m": round(float(uncertainty), 3),
            "nearest_site_id": nearest_id,
            "nearest_distance_deg": round(nearest_distance, 4),
        })
    return rows


def write_prediction_csv(path: str, rows: list[dict]) -> None:
    headers = [
        "latitude",
        "longitude",
        "elevation_m",
        "predicted_water_level_m_bgl",
        "uncertainty_m",
        "nearest_site_id",
        "nearest_distance_deg",
    ]
    with open(path, "w", newline="", encoding="utf-8") as handle:
        writer = csv.DictWriter(handle, fieldnames=headers)
        writer.writeheader()
        writer.writerows(rows)


def write_prediction_plot(path: str, rows: list[dict], observations: list[dict]) -> None:
    fig, ax = plt.subplots(figsize=(8, 5), constrained_layout=True)
    scatter = ax.scatter(
        [row["longitude"] for row in rows],
        [row["latitude"] for row in rows],
        c=[row["predicted_water_level_m_bgl"] for row in rows],
        s=160,
        cmap="viridis_r",
        edgecolors="#111827",
        linewidths=0.5,
    )
    ax.scatter(
        [obs["longitude"] for obs in observations],
        [obs["latitude"] for obs in observations],
        c="#ef4444",
        s=22,
        marker="x",
        label="observations",
    )
    ax.set_title("Honshu groundwater prediction surface")
    ax.set_xlabel("Longitude")
    ax.set_ylabel("Latitude")
    ax.legend(loc="lower left")
    colorbar = fig.colorbar(scatter, ax=ax)
    colorbar.set_label("Predicted water level (m below ground)")
    fig.savefig(path, format="svg")
    plt.close(fig)


def extract_last_json_block(text: str) -> str:
    matches = list(re.finditer(r"```json\s*(.*?)```", text, flags=re.DOTALL))
    if matches:
        return matches[-1].group(1).strip()
    start = text.find("{")
    end = text.rfind("}")
    if start >= 0 and end > start:
        return text[start:end + 1].strip()
    raise ValueError("no JSON object found")


def default_artifacts_dir() -> str:
    root = os.environ.get("DANGO_ARTIFACTS_DIR", "")
    if not root:
        return ""
    return os.path.join(root, "train_gp_model")


def pseudo_elevation(lat: float, lon: float) -> float:
    mountain = 850 * math.exp(-(((lat - 36.4) ** 2) / 5.8 + ((lon - 138.1) ** 2) / 4.4))
    regional = 55 * math.sin(lat * 0.9) + 35 * math.cos(lon * 0.8)
    coastal = 25 * abs(math.sin((lon - lat) * 0.35))
    return max(0.0, 45 + mountain + regional + coastal)


def nearest_observation(lat: float, lon: float, observations: list[dict]) -> tuple[str, float]:
    nearest_id = ""
    nearest_distance = float("inf")
    for obs in observations:
        dist = math.hypot(lat - obs["latitude"], lon - obs["longitude"])
        if dist < nearest_distance:
            nearest_distance = dist
            nearest_id = obs["site_id"]
    return nearest_id, nearest_distance


def mean(values) -> float:
    values = list(values)
    return sum(values) / len(values)


if __name__ == "__main__":
    raise SystemExit(main())
