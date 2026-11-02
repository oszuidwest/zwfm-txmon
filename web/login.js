// Icons and the show-password toggle of the login page, as in zwfm-encoder.
document.getElementById("login-logo").innerHTML = window.icons.appLogo;
document.getElementById("github-icon").innerHTML = window.icons.github;
document.getElementById("eye-icon").innerHTML = window.icons.eye;

const warningIcon = document.getElementById("warning-icon");
if (warningIcon) {
    warningIcon.innerHTML = window.icons.warning;
    document.getElementById("login-error").focus();
}

const passwordInput = document.getElementById("password");
const toggleBtn = document.getElementById("password-toggle");
const eyeIcon = document.getElementById("eye-icon");

toggleBtn.addEventListener("click", () => {
    const isPassword = passwordInput.type === "password";
    passwordInput.type = isPassword ? "text" : "password";
    eyeIcon.innerHTML = isPassword ? window.icons.eyeSlash : window.icons.eye;
    toggleBtn.setAttribute("aria-label", isPassword ? "Hide Password" : "Show Password");
});
