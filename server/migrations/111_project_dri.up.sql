-- project.dri_user_id formalizes "the human accountable for outcomes."
-- Distinct from lead_type / lead_id, which is polymorphic ("who is
-- actively driving execution" — can be a member, an agent, or a squad).
-- DRI is always a person; the FK type guarantees that structurally.
-- SOP v0.4 P-5: every project has exactly one DRI.
--
-- Naming follows the convention established in 110_skill_owner_and_review:
-- the `_user_id` suffix is explicit that this FK targets `"user"`, leaving
-- the bare `dri_id` / `owner_id` names available if ownership ever needs
-- to become polymorphic.

ALTER TABLE project
    ADD COLUMN dri_user_id UUID REFERENCES "user"(id) ON DELETE SET NULL;

CREATE INDEX idx_project_dri_user_id ON project(dri_user_id);
