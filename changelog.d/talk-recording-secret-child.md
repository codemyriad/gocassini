### Fixed
- The operator now hands its Talk recording secret to the recorder it starts. Since D-447 the operator generates that secret when the administrator sets none, and the recorder never received it, so on such installs Talk showed a working recording backend and every recording failed with "talk auth mode hpb-internal requires CASSINI_TALK_RECORDING_SECRET to be set".
