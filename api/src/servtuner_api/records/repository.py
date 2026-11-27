"""All the record classes as one repository, which services take."""

from servtuner_api.records.experiments import ExperimentRecords
from servtuner_api.records.runs import RunRecords
from servtuner_api.records.schedules import ScheduleRecords
from servtuner_api.records.targets import TargetRecords
from servtuner_api.records.tuning import TuningRecords


class RunRepository(RunRecords, ExperimentRecords, TargetRecords, TuningRecords, ScheduleRecords):
    """Every query and state transition the api makes, by domain."""
