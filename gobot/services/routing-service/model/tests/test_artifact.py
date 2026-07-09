from model.artifact import write_artifact, load_artifact

def test_roundtrip(tmp_path):
    p = tmp_path / "m.json"
    a = write_artifact(str(p), {"LIMITED|WEAK": {"sell_decay_per_step": 0.95, "n_obs": 5}},
                       {"WEAK": {"half_life_minutes": 60, "n_series": 2}},
                       era="torwind-2026-07-05", generated_at="2026-07-09T22:00:00Z")
    b = load_artifact(str(p))
    assert b == a and b["fit_version"] == 1 and b["era"] == "torwind-2026-07-05"
