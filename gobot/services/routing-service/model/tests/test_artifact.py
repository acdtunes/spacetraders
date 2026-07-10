from model.artifact import write_artifact, load_artifact, mark_thin_sides

def test_roundtrip(tmp_path):
    p = tmp_path / "m.json"
    a = write_artifact(str(p), {"LIMITED|WEAK": {"sell_decay_per_step": 0.95, "n_obs": 5}},
                       {"WEAK": {"half_life_minutes": 60, "n_series": 2}},
                       era="torwind-2026-07-05", generated_at="2026-07-09T22:00:00Z")
    b = load_artifact(str(p))
    assert b == a and b["fit_version"] == 2 and b["era"] == "torwind-2026-07-05"

def test_extra_diagnostics_merged(tmp_path):
    # sp-bkjz: tier_at_time coverage rides alongside the base diagnostics.
    p = tmp_path / "m.json"
    a = write_artifact(str(p), {"LIMITED|WEAK": {"sell_decay_per_step": 0.95, "n_obs": 5}},
                       {"WEAK": {"half_life_minutes": 60, "n_series": 2}},
                       era="torwind-2026-07-05", generated_at="2026-07-09T22:00:00Z",
                       extra_diagnostics={"tier_at_time": {"steps_true": 4, "steps_total": 5,
                                                           "coverage": 0.8}})
    b = load_artifact(str(p))
    assert b["diagnostics"]["ladder_count"] == 5          # base diagnostics preserved
    assert b["diagnostics"]["tier_at_time"]["coverage"] == 0.8
    assert b["diagnostics"]["tier_at_time"]["steps_true"] == 4

def test_mark_thin_sides_flags_only_underpopulated_sides():
    # sp-bkjz: a side below the coverage floor is flagged thin in the artifact so
    # consumers don't trust it; a well-populated side is left unflagged. Input untouched.
    impact = {
        "HIGH|GROWING": {"sell_decay_per_step": 0.79, "sell_n_obs": 23,   # thin
                         "buy_growth_per_step": 1.10, "buy_n_obs": 200},  # populated
        "SCARCE|WEAK": {"sell_decay_per_step": 0.96, "sell_n_obs": 257},  # populated
    }
    marked = mark_thin_sides(impact, min_obs=30)
    assert marked["HIGH|GROWING"]["sell_thin"] is True
    assert "buy_thin" not in marked["HIGH|GROWING"]
    assert "sell_thin" not in marked["SCARCE|WEAK"]
    assert "sell_thin" not in impact["HIGH|GROWING"]      # original not mutated
