@0x9bc444c453564da5;
using Go = import "/go.capnp";

$Go.package("caps");

annotation required(field) :Text;
annotation ignored(field) :Void;
annotation optional(field) :Text;
