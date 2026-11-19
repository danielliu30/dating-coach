import pytest
from pydantic import ValidationError

from app.schemas import AnalyzeResponse, ProfileEvidence, ProfileObservation


def test_older_result_does_not_require_profile_evidence():
    result = AnalyzeResponse.model_validate({"model_version": "old", "overall": {"engagement_score": 0.5}})
    assert result.overall.profile_evidence is None


def test_profile_evidence_round_trip_preserves_citations_and_version():
    result = AnalyzeResponse.model_validate(
        {
            "model_version": "test-model",
            "overall": {
                "engagement_score": 0.5,
                "profile_evidence": {
                    "coverage": "eligible",
                    "self_correction": {"level": "not_applicable"},
                    "engagement": {"level": "strong", "self_positions": [2, 4]},
                },
            },
        }
    )
    assert result.overall.profile_evidence is not None
    assert result.overall.profile_evidence.rubric_version == "profile-v1"
    assert result.overall.profile_evidence.engagement.self_positions == [2, 4]
    assert AnalyzeResponse.model_validate_json(result.model_dump_json()) == result


@pytest.mark.parametrize(
    "observation",
    [
        {"level": "strong"},
        {"level": "strong", "self_positions": [-1]},
        {"level": "strong", "self_positions": [4, 2]},
        {"level": "strong", "self_positions": [2, 2]},
        {"level": "strong", "self_positions": [2], "issue": "repeated_ignored_constraint"},
        {"level": "needs_coaching", "self_positions": [2]},
        {"level": "unknown", "self_positions": [2]},
        {"level": "strong", "self_positions": [2], "prose": "Untrusted evidence"},
    ],
)
def test_invalid_observations_are_rejected(observation):
    with pytest.raises(ValidationError):
        ProfileObservation.model_validate(observation)


def test_insufficient_conversation_cannot_be_assessed():
    with pytest.raises(ValidationError):
        ProfileEvidence(coverage="insufficient", engagement=ProfileObservation(level="strong", self_positions=[2]))


def test_not_applicable_is_only_for_self_correction():
    with pytest.raises(ValidationError):
        ProfileEvidence(coverage="eligible", engagement=ProfileObservation(level="not_applicable"))
