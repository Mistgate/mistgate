package store

// SettingClientAPI is the owner's switch of the client API; absent = off. It opens the discovery header on subscription app
// answers and enrolment by the link (POST <link>/client/enrol), nothing else: a device that is already enrolled does not depend
// on it (the refusal of a basic fetch that carries its HWID is made whatever the switch says, and the device's own calls will
// be too).
const SettingClientAPI = "client_api.enabled"
