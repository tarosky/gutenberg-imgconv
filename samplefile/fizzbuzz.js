// FizzBuzz sample program
(function () {
  for (var counter = 1; counter <= 20; counter++) {
    if (counter % 15 == 0) {
      console.log("FizzBuzz");
    } else if (counter % 3 == 0) {
      console.log("Fizz");
    } else if (counter % 5 == 0) {
      console.log("Buzz");
    } else {
      console.log(counter);
    }
  }
})();
