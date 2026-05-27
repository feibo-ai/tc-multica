-- skill.owner_user_id is the human currently accountable for the skill.
-- Distinct from created_by, which is historical (who first wrote it).
-- Ownership can be re-assigned over time; created_by is immutable.
-- last_reviewed_at backs the "stale skill" check: a skill not reviewed in
-- N days is surfaced by `multica skill list --stale` and by the MCP
-- skill_lint tool. SOP v0.4 anti-pattern ❌5: skills without an owner or
-- a recent review are a stale-skill risk.

ALTER TABLE skill
    ADD COLUMN owner_user_id UUID REFERENCES "user"(id) ON DELETE SET NULL,
    ADD COLUMN last_reviewed_at TIMESTAMPTZ;

CREATE INDEX idx_skill_owner_user_id ON skill(owner_user_id);
CREATE INDEX idx_skill_last_reviewed_at ON skill(last_reviewed_at);
