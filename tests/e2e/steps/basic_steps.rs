use cucumber::then;

use crate::common::utils::cleanup_container;
use crate::VibeToolWorld;

#[then("I should clean up the test resources")]
fn cleanup_resources(world: &mut VibeToolWorld) {
    // Clean up the container if it exists
    if let Some(ref container_id) = world.container_id {
        let _ = cleanup_container(container_id);
    }

    // Reset the world state
    world.command_output = None;
    world.container_id = None;
    world.server_name = None;
    world.transport_type = None;
    world.port = None;
    world.error_message = None;
}

#[then(expr = "I should see {string} in the output")]
fn see_in_output(world: &mut VibeToolWorld, expected: String) {
    if let Some(ref output) = world.command_output {
        let stdout = String::from_utf8_lossy(&output.stdout);
        let stderr = String::from_utf8_lossy(&output.stderr);

        assert!(
            stdout.contains(&expected) || stderr.contains(&expected),
            "Expected output to contain '{}', but got:\nSTDOUT: {}\nSTDERR: {}",
            expected,
            stdout,
            stderr
        );
    } else {
        panic!("No command output available");
    }
}

#[then(expr = "I should not see {string} in the output")]
fn not_see_in_output(world: &mut VibeToolWorld, unexpected: String) {
    if let Some(ref output) = world.command_output {
        let stdout = String::from_utf8_lossy(&output.stdout);
        let stderr = String::from_utf8_lossy(&output.stderr);

        assert!(
            !stdout.contains(&unexpected) && !stderr.contains(&unexpected),
            "Expected output to not contain '{}', but it did:\nSTDOUT: {}\nSTDERR: {}",
            unexpected,
            stdout,
            stderr
        );
    } else {
        panic!("No command output available");
    }
}
